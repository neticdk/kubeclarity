// Copyright © 2022 Cisco Systems, Inc. and its affiliates.
// All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package syft

import (
	"context"
	"fmt"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/cataloging"
	"github.com/anchore/syft/syft/format/common/cyclonedxhelpers"
	syft_sbom "github.com/anchore/syft/syft/sbom"
	"github.com/anchore/syft/syft/source"
	log "github.com/sirupsen/logrus"

	"github.com/openclarity/kubeclarity/shared/pkg/analyzer"
	"github.com/openclarity/kubeclarity/shared/pkg/config"
	"github.com/openclarity/kubeclarity/shared/pkg/job_manager"
	"github.com/openclarity/kubeclarity/shared/pkg/utils"
	"github.com/openclarity/kubeclarity/shared/pkg/utils/image_helper"
)

const AnalyzerName = "syft"

type Analyzer struct {
	name       string
	logger     *log.Entry
	config     config.SyftConfig
	resultChan chan job_manager.Result
	localImage bool
}

func New(c job_manager.IsConfig, logger *log.Entry, resultChan chan job_manager.Result) job_manager.Job {
	conf := c.(*config.Config) // nolint:forcetypeassert
	return &Analyzer{
		name:       AnalyzerName,
		logger:     logger.Dup().WithField("analyzer", AnalyzerName),
		config:     config.CreateSyftConfig(conf.Analyzer, conf.Registry),
		resultChan: resultChan,
		localImage: conf.LocalImageScan,
	}
}

func (a *Analyzer) Run(sourceType utils.SourceType, userInput string) error {
	src := utils.CreateSource(sourceType, userInput, a.localImage)
	a.logger.Infof("Called %s analyzer on source %s", a.name, src)

	sc := syft.DefaultGetSourceConfig().WithRegistryOptions(a.config.RegistryOptions)
	s, err := syft.GetSource(context.Background(), src, sc)
	if err != nil {
		return fmt.Errorf("failed to create source analyzer=%s: %v", a.name, err)
	}

	go func() {
		res := &analyzer.Results{}

		cc := syft.DefaultCreateSBOMConfig().WithSearchConfig(cataloging.DefaultSearchConfig().WithScope(a.config.Scope))
		sbom, err := syft.CreateSBOM(context.Background(), s, cc)
		if err != nil {
			a.setError(res, fmt.Errorf("failed to generate sbom: %w", err))
			return
		}

		cdxBom := cyclonedxhelpers.ToFormatModel(*sbom)
		res = analyzer.CreateResults(cdxBom, a.name, src, sourceType)

		// Syft uses ManifestDigest to fill version information in the case of an image.
		// We need RepoDigest/ImageID as well which is not set by Syft if we're using cycloneDX output.
		// Get the RepoDigest/ImageID from image metadata and use it as SourceHash in the Result
		// that will be added to the component hash of metadata during the merge.
		switch sourceType {
		case utils.IMAGE, utils.DOCKERARCHIVE, utils.OCIDIR, utils.OCIARCHIVE:
			if res.AppInfo.SourceHash, err = getImageHash(sbom, userInput); err != nil {
				a.setError(res, fmt.Errorf("failed to get image hash: %v", err))
				return
			}
		case utils.SBOM, utils.DIR, utils.ROOTFS, utils.FILE:
			// ignore
		default:
			// ignore
		}

		a.logger.Infof("Sending successful results")
		a.resultChan <- res
	}()

	return nil
}

func (a *Analyzer) setError(res *analyzer.Results, err error) {
	res.Error = err
	a.logger.Error(res.Error)
	a.resultChan <- res
}

func getImageHash(sbom *syft_sbom.SBOM, src string) (string, error) {
	switch metadata := sbom.Source.Metadata.(type) {
	case source.ImageMetadata:
		hash, err := image_helper.GetHashFromRepoDigestsOrImageID(metadata.RepoDigests, metadata.ID, src)
		if err != nil {
			return "", fmt.Errorf("failed to get image hash from repo digests or image id: %w", err)
		}
		return hash, nil
	default:
		return "", fmt.Errorf("failed to get image hash from source metadata")
	}
}

package s3

import (
	zen_targets "github.com/zen-io/zen-core/target"
)

var KnownTargets = zen_targets.TargetCreatorMap{
	"s3_file": S3FileConfig{},
}

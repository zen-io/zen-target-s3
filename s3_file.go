package s3

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	environs "github.com/zen-io/zen-core/environments"
	zen_targets "github.com/zen-io/zen-core/target"
	"github.com/zen-io/zen-core/utils"
	archives "github.com/zen-io/zen-target-archiving"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3FileConfig struct {
	Name         string                           `mapstructure:"name" desc:"Name for the target"`
	Description  string                           `mapstructure:"desc" desc:"Target description"`
	Labels       []string                         `mapstructure:"labels" desc:"Labels to apply to the targets"` //
	Deps         []string                         `mapstructure:"deps" desc:"Build dependencies"`
	PassEnv      []string                         `mapstructure:"pass_env" desc:"List of environment variable names that will be passed from the OS environment, they are part of the target hash"`
	SecretEnv    []string                         `mapstructure:"secret_env" desc:"List of environment variable names that will be passed from the OS environment, they are not used to calculate the target hash"`
	Env          map[string]string                `mapstructure:"env" desc:"Key-Value map of static environment variables to be used"`
	Tools        map[string]string                `mapstructure:"tools" desc:"Key-Value map of tools to include when executing this target. Values can be references"`
	Visibility   []string                         `mapstructure:"visibility" desc:"List of visibility for this target"`
	Environments map[string]*environs.Environment `mapstructure:"environments" desc:"Deployment Environments"`

	Srcs      []string `mapstructure:"srcs"`
	Zip       *string  `mapstructure:"zip"`
	Bucket    string   `mapstructure:"bucket"`
	BucketKey string   `mapstructure:"bucket_key"`
}

func (fc S3FileConfig) GetTargets(tcc *zen_targets.TargetConfigContext) ([]*zen_targets.Target, error) {
	out := filepath.Base(fc.BucketKey)
	srcs := map[string][]string{
		"zip": {},
	}

	steps := []*zen_targets.Target{}
	if len(fc.Srcs) != 0 {
		acName := fmt.Sprintf("%s_archive", fc.Name)

		ac, err := archives.ArchiveConfig{
			Name:       acName,
			Out:        "archive.zip",
			Type:       "zip",
			Srcs:       fc.Srcs,
			Labels:     fc.Labels,
			Deps:       fc.Deps,
			Visibility: []string{fc.Name},
			PassEnv:    fc.PassEnv,
			SecretEnv:  fc.SecretEnv,
			Env:        fc.Env,
		}.GetTargets(tcc)
		if err != nil {
			return nil, err
		}

		steps = append(steps, ac...)
		srcs["zip"] = []string{fmt.Sprintf(":%s", acName)}
		fc.Deps = append(fc.Deps, fmt.Sprintf(":%s", acName))
	} else if fc.Zip != nil {
		srcs["zip"] = []string{*fc.Zip}
	} else {
		return nil, fmt.Errorf("provide either a Zip or Srcs")
	}

	steps = append(steps,
		zen_targets.NewTarget(
			fc.Name,
			zen_targets.WithOuts([]string{out}),
			zen_targets.WithEnvironments(fc.Environments),
			zen_targets.WithSrcs(srcs),
			zen_targets.WithTargetScript("build", &zen_targets.TargetScript{
				Deps: fc.Deps,
				Run: func(target *zen_targets.Target, runCtx *zen_targets.RuntimeContext) error {
					return utils.Copy(target.Srcs["zip"][0], filepath.Join(target.Cwd, target.Outs[0]))
				},
			}),

			zen_targets.WithTargetScript("deploy", &zen_targets.TargetScript{
				Run: func(target *zen_targets.Target, runCtx *zen_targets.RuntimeContext) error {
					target.SetStatus("Uploading to s3 (%s)", target.Qn())
					if !runCtx.DryRun {
						var file *os.File
						if f, err := os.Open(target.Outs[0]); err != nil {
							return fmt.Errorf("opening src %s: %w", target.Outs[0], err)
						} else {
							file = f
							defer file.Close()
						}

						customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
							var endpoint string
							if val, ok := target.EnvVars()["AWS_S3_ENDPOINT"]; ok {
								endpoint = val
							} else {
								endpoint = "https://s3.eu-central-1.amazonaws.com"
							}
							if service == s3.ServiceID && region == "eu-central-1" {
								return aws.Endpoint{
									PartitionID:   "aws",
									URL:           endpoint,
									SigningRegion: "eu-central-1",
								}, nil
							}
							// returning EndpointNotFoundError will allow the service to fallback to it's default resolution
							return aws.Endpoint{}, &aws.EndpointNotFoundError{}
						})
						cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithEndpointResolverWithOptions(customResolver))
						if err != nil {
							return fmt.Errorf("loading aws config: %w", err)
						}

						client := s3.NewFromConfig(cfg, func(o *s3.Options) {
							o.UsePathStyle = true
						})
						interpolatedBucket, err := target.Interpolate(fc.Bucket)
						target.Debugln("Bucket: %s", interpolatedBucket)
						if err != nil {
							return fmt.Errorf("interpolating bucket name: %w", err)
						}
						interpolatedBucketKey, err := target.Interpolate(fc.BucketKey)
						if err != nil {
							return fmt.Errorf("interpolating bucket key: %w", err)
						}
						target.Debugln("Bucket key: %s", interpolatedBucketKey)

						// Upload the file to S3
						if _, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
							Bucket: aws.String(interpolatedBucket),
							Key:    aws.String(interpolatedBucketKey),
							Body:   file,
						}); err != nil {
							return fmt.Errorf("uploading: %w", err)
						}
					}

					return nil
				},
			}),

			zen_targets.WithTargetScript("remove", &zen_targets.TargetScript{
				Run: func(target *zen_targets.Target, runCtx *zen_targets.RuntimeContext) error {
					// TODO

					// if runCtx.Env == "" {
					// 	if len(tc.Environments) > 0 {
					// 		return fmt.Errorf("you must specify an environment to deploy")
					// 	}

					// 	runCtx.Env = "_default"
					// }

					// execEnv := runCtx.GetEnvironmentVariables(ctx, tc.Environments[runCtx.Env])

					// if err := initFn(ctx, runCtx.Env, execEnv); err != nil {
					// 	return fmt.Errorf("executing init: %s", err)
					// }

					// if runCtx.DryRun {
					// 	if err := planFn(ctx, runCtx.Env, execEnv, true); err != nil {
					// 		return fmt.Errorf("executing plan: %s", err)
					// 	}
					// } else {

					// 	applyCmd := exec.Command(ctx.Tools["terraform"], "destroy", "-auto-approve")
					// 	applyCmd.Dir = fmt.Sprintf("%s/%s", ctx.Cwd, runCtx.Env)
					// 	applyCmd.Env = execEnv
					// 	applyCmd.Stdout = ctx.Out.GetLogger()
					// 	applyCmd.Stderr = ctx.Out.GetLogger()
					// 	if err := applyCmd.Run(); err != nil {
					// 		return fmt.Errorf("running apply: %s", err.Error())
					// 	}
					// }

					return nil
				},
			}),
		),
	)

	return steps, nil
}

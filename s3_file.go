package s3

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	zen_targets "github.com/zen-io/zen-core/target"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3FileConfig struct {
	zen_targets.BaseFields   `mapstructure:",squash"`
	zen_targets.DeployFields `mapstructure:",squash"`
	Srcs                     []string `mapstructure:"srcs"`
	Bucket                   string   `mapstructure:"bucket"`
	BucketKey                string   `mapstructure:"bucket_key"`
}

func (fc S3FileConfig) GetTargets(_ *zen_targets.TargetConfigContext) ([]*zen_targets.Target, error) {
	out := filepath.Base(fc.BucketKey)
	srcs := map[string][]string{
		"_srcs": {},
	}

	srcs["_srcs"] = append(srcs["_srcs"], fc.Srcs...)

	steps := []*zen_targets.Target{
		zen_targets.NewTarget(
			fc.Name,
			zen_targets.WithOuts([]string{out}),
			zen_targets.WithEnvironments(fc.Environments),
			zen_targets.WithSrcs(srcs),
			zen_targets.WithTargetScript("build", &zen_targets.TargetScript{
				Deps: fc.Deps,
				Run: func(target *zen_targets.Target, runCtx *zen_targets.RuntimeContext) error {
					var archive *os.File
					if a, err := os.Create(filepath.Join(target.Cwd, target.Outs[0])); err != nil {
						return nil
					} else {
						archive = a
						defer archive.Close()
					}

					zipWriter := zip.NewWriter(archive)

					for _, src := range target.Srcs["_srcs"] {
						if f1, err := os.Open(src); err != nil {
							return nil
						} else if w1, err := zipWriter.Create(src); err != nil {
							return err
						} else if _, err := io.Copy(w1, f1); err != nil {
							return err
						} else {
							f1.Close()
						}
					}

					for _, src := range target.Srcs["_deps"] {
						parts := strings.Split(src, "/")
						var to string
						if len(parts) > 1 {
							to = strings.Join(parts[1:], "/")
						} else {
							to = src
						}
						if f1, err := os.Open(src); err != nil {
							return err
						} else if w1, err := zipWriter.Create(to); err != nil {
							return err
						} else if _, err := io.Copy(w1, f1); err != nil {
							return err
						} else {
							f1.Close()
						}
					}

					zipWriter.Close()

					return nil
				},
			}),

			zen_targets.WithTargetScript("deploy", &zen_targets.TargetScript{
				Run: func(target *zen_targets.Target, runCtx *zen_targets.RuntimeContext) error {
					target.SetStatus("Uploading to s3 (%s)", target.Qn())
					if !runCtx.DryRun {
						var file *os.File
						if f, err := os.Open(filepath.Join(target.Cwd, target.Outs[0])); err != nil {
							return fmt.Errorf("opening src %s: %w", filepath.Join(target.Cwd, target.Outs[0]), err)
						} else {
							file = f
							defer file.Close()
						}

						customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
							var endpoint string
							if val, ok := target.EnvVars()["AWS_S3_ENDPOINT"]; ok {
								endpoint = val
							} else {
								endpoint = "s3.eu-central-1.amazonaws.com"
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
	}

	return steps, nil
}

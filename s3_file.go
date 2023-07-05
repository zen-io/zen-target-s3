package s3

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	environs "github.com/zen-io/zen-core/environments"
	zen_targets "github.com/zen-io/zen-core/target"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
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
	MaxParallel  *int                             `mapstructure:"max_parallel" desc:"Maximum number of parallel uploads. Defaults to 10"`
	Srcs         []string                         `mapstructure:"srcs"`
	Bucket       string                           `mapstructure:"bucket"`
	BucketPrefix string                           `mapstructure:"bucket_prefix"`
}

func (fc S3FileConfig) GetTargets(tcc *zen_targets.TargetConfigContext) ([]*zen_targets.Target, error) {
	if fc.MaxParallel == nil {
		fc.MaxParallel = new(int)
		*fc.MaxParallel = 10
	}
	steps := []*zen_targets.Target{
		zen_targets.NewTarget(
			fc.Name,
			zen_targets.WithOuts([]string{"**/*"}),
			zen_targets.WithEnvironments(fc.Environments),
			zen_targets.WithSrcs(map[string][]string{
				"_srcs": fc.Srcs,
			}),
			zen_targets.WithTargetScript("build", &zen_targets.TargetScript{
				Deps: fc.Deps,
			}),
			zen_targets.WithTargetScript("deploy", &zen_targets.TargetScript{
				Run: func(target *zen_targets.Target, runCtx *zen_targets.RuntimeContext) error {
					target.SetStatus("Uploading to s3 (%s)", target.Qn())
					if !runCtx.DryRun {
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

						interpolatedBucketPrefix, err := target.Interpolate(fc.BucketPrefix)
						if err != nil {
							return fmt.Errorf("interpolating bucket key: %w", err)
						}

						target.Debugln("Bucket key: %s", interpolatedBucketPrefix)

						// Create an uploader with the S3 client and default options
						uploader := manager.NewUploader(client)

						// Create a WaitGroup to manage concurrency
						var wg sync.WaitGroup

						// Create a buffered channel to control concurrency
						sem := make(chan struct{}, *fc.MaxParallel)

						for _, out := range target.Outs {
							wg.Add(1)

							// Acquire a token from the semaphore
							sem <- struct{}{}

							go func(f string) {
								// Decrement the counter when the goroutine completes
								defer wg.Done()

								// Open the file for use
								file, err := os.Open(f)
								if err != nil {
									log.Fatalf("failed to open file %q, %v", f, err)
								}
								defer file.Close()

								// Use the uploader to upload the file
								_, err = uploader.Upload(context.TODO(), &s3.PutObjectInput{
									Bucket: aws.String(interpolatedBucket),
									Key:    aws.String(filepath.Join(interpolatedBucketPrefix, strings.TrimPrefix(f, target.Cwd))),
									Body:   file,
								})
								if err != nil {
									log.Fatalf("failed to upload file %q, %v", f, err)
								}

								target.Debugln("successfully uploaded %q to S3\n", f)

								// Release a token back to the semaphore
								<-sem
							}(out)
						}

						// Wait for all uploads to complete
						wg.Wait()
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

package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

type VersionKey struct {
	Name      string
	VersionId string
}

// Deletes data from bucket including versions
// Example usage would be as follows
// AwsRegion=ap-southeast-2 S3Bucket=my.bucket S3Prefix=191 go run main.go
// Which will load delete all records with the bucket with the prefix 191
func main() {
	parseFlags()
	err := purge()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

var (
	region      string
	bucket      string
	prefix      string
	concurrency int
)

func parseFlags() {
	flag.StringVar(&region, "region", "", "Provide region name, e.g. '-region ap-southeast-2'")
	flag.StringVar(&bucket, "bucket", "", "Enter bucket name, e.g. '-bucket my.bucket-name'")
	flag.StringVar(&prefix, "prefix", "", "If we should purge objects with specific prefix (behind dir structure)")
	flag.IntVar(&concurrency, "concurrency", 300, "")
	flag.Parse()
}

func purge() error {
	err := validateInput()
	if err != nil {
		return err
	}

	svc, err := session.NewSession(&aws.Config{Region: &region})
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	s3client := s3.New(svc)
	s3ListQueue := make(chan string, 100000)
	s3VersionListQueue := make(chan VersionKey, 100000)

	// Get the keys from S3
	wg.Add(1)
	go func() {
		err = s3client.ListObjectsPages(&s3.ListObjectsInput{
			Bucket: &bucket,
			Prefix: &prefix,
		}, func(page *s3.ListObjectsOutput, lastPage bool) bool {
			for _, value := range page.Contents {
				s3ListQueue <- *value.Key
			}
			return true
		})

		if err != nil {
			slog.Error(err.Error())
		}

		close(s3ListQueue)
		wg.Done()
	}()

	// Get all the versions from S3
	wg.Add(1)
	go func() {
		err = s3client.ListObjectVersionsPages(&s3.ListObjectVersionsInput{
			Bucket: &bucket,
			Prefix: &prefix,
		}, func(page *s3.ListObjectVersionsOutput, lastPage bool) bool {
			for _, value := range page.Versions {
				s3VersionListQueue <- VersionKey{
					Name:      *value.Key,
					VersionId: *value.VersionId,
				}
			}

			for _, value := range page.DeleteMarkers {
				s3VersionListQueue <- VersionKey{
					Name:      *value.Key,
					VersionId: *value.VersionId,
				}
			}

			return true
		})

		if err != nil {
			slog.Error(err.Error())
		}

		close(s3VersionListQueue)
		wg.Done()
	}()

	for range concurrency {
		wg.Add(1)
		go func(input chan string) {
			for key := range input {
				slog.Info("Purging", "key", key)

				_, err := s3client.DeleteObject(&s3.DeleteObjectInput{
					Bucket: &bucket,
					Key:    &key,
				})
				if err != nil {
					slog.Error(fmt.Sprintf("Couldn't delete object %s", key))
				}
			}
			wg.Done()
		}(s3ListQueue)
	}

	for range concurrency {
		wg.Add(1)
		go func(input chan VersionKey) {
			for key := range input {
				slog.Info("Purging", "Key", key.Name, "Version", key.VersionId)

				_, err := s3client.DeleteObject(&s3.DeleteObjectInput{
					Bucket:    &bucket,
					Key:       &key.Name,
					VersionId: &key.VersionId,
				})
				if err != nil {
					slog.Error(fmt.Sprintf("Couldn't delete object %s, version %s", key.Name, key.VersionId))
				}
			}
			wg.Done()
		}(s3VersionListQueue)
	}

	slog.Info("Starting...", "Bucket", bucket, "Region", region, "Prefix", prefix)
	wg.Wait()
	slog.Info("Finished", "Bucket", bucket, "Region", region, "Prefix", prefix)
	return nil
}

func validateInput() error {
	if region == "" {
		flag.PrintDefaults()
		return errors.New("region not provided")
	}
	if bucket == "" {
		flag.PrintDefaults()
		return errors.New("region / bucket not provided")
	}

	return nil
}

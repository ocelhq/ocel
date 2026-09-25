package s3

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	orphanAge      = 24 * time.Hour
	sweepInterval  = time.Hour
	sweepWindow    = 30 * time.Second
	sweptPerSweep  = 1000
	sweepPageLimit = 4
)

func (s *Service) sweepable(held scope) bool {
	if !s.cfg.SweepUploads || s.cfg.Objects == nil {
		return false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, swept := s.swept[held.bucket+"/"+held.prefix]; swept && now.Sub(last) < sweepInterval {
		return false
	}
	if s.swept == nil {
		s.swept = map[string]time.Time{}
	}
	s.swept[held.bucket+"/"+held.prefix] = now
	return true
}

func (s *Service) sweep(held scope) {
	if !s.sweepable(held) {
		return
	}
	s.sweeping(func() {
		ctx, stop := context.WithTimeout(context.WithoutCancel(context.Background()), sweepWindow)
		defer stop()
		s.sweepUploads(ctx, held)
	})
}

func (s *Service) sweepUploads(ctx context.Context, held scope) {
	abandoned := s.now().Add(-orphanAge)
	in := &s3.ListMultipartUploadsInput{
		Bucket:     aws.String(held.bucket),
		MaxUploads: aws.Int32(sweptPerSweep),
	}
	if held.prefix != "" {
		in.Prefix = aws.String(held.prefix)
	}
	for page := 0; page < sweepPageLimit; page++ {
		out, err := s.cfg.Objects.ListMultipartUploads(ctx, in)
		if err != nil {
			return
		}
		for _, upload := range out.Uploads {
			if upload.Initiated == nil || upload.Initiated.After(abandoned) {
				continue
			}
			if _, err := s.cfg.Objects.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(held.bucket),
				Key:      upload.Key,
				UploadId: upload.UploadId,
			}); err != nil {
				return
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return
		}
		in.KeyMarker, in.UploadIdMarker = out.NextKeyMarker, out.NextUploadIdMarker
	}
}

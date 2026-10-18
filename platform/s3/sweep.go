package s3

import (
	"context"
	"maps"
	"slices"
	"strings"
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

func (s *Service) due(sweep string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, swept := s.swept[sweep]; swept && now.Sub(last) < sweepInterval {
		return false
	}
	if s.swept == nil {
		s.swept = map[string]time.Time{}
	}
	s.swept[sweep] = now
	return true
}

func (s *Service) inBackground(sweep string, run func(context.Context)) {
	if s.cfg.Objects == nil || !s.due(sweep) {
		return
	}
	s.sweeping(func() {
		ctx, stop := context.WithTimeout(context.WithoutCancel(context.Background()), sweepWindow)
		defer stop()
		run(ctx)
	})
}

func (s *Service) sweep(granted scope) {
	if !s.cfg.SweepUploads {
		return
	}
	s.inBackground("uploads "+granted.bucket+"/"+granted.prefix, func(ctx context.Context) { s.sweepUploads(ctx, granted) })
}

func (s *Service) sweepSessions(ctx context.Context) {
	cutoff := s.now().Add(-sessionRecordLifetime)
	pages := sweepPageLimit
	for _, bucket := range slices.Sorted(maps.Keys(s.granted)) {
		if !s.sweepSessionsOf(ctx, bucket, cutoff, &pages) {
			return
		}
	}
}

func (s *Service) sweepSessionsOf(ctx context.Context, bucket string, cutoff time.Time, pages *int) bool {
	owned := ownedSessionPrefix(s.cfg.Tag, bucket)
	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.sessions.bucket),
		Prefix:  aws.String(s.sessions.key(sessionKey(owned))),
		MaxKeys: aws.Int32(sweptPerSweep),
	}
	for *pages > 0 {
		*pages--
		out, err := s.cfg.Objects.ListObjectsV2(ctx, in)
		if err != nil {
			return false
		}
		var settled []string
		reachedYoung := false
		for _, object := range out.Contents {
			id := strings.TrimPrefix(s.sessions.strip(aws.ToString(object.Key)), SessionKeyPrefix)
			openedAt, stamped := sessionOpenedAt(owned, id)
			if !stamped {
				continue
			}
			if openedAt.After(cutoff) {
				reachedYoung = true
				break
			}
			if s.settleAbandoned(ctx, id) {
				settled = append(settled, sessionKey(id))
			}
		}
		if err := s.remove(ctx, s.sessions, settled); err != nil {
			return false
		}
		if reachedYoung || aws.ToString(out.NextContinuationToken) == "" {
			return true
		}
		in.ContinuationToken = out.NextContinuationToken
	}
	return false
}

func (s *Service) settleAbandoned(ctx context.Context, id string) bool {
	sess, granted, err := s.readGrantedSession(ctx, id)
	if err != nil || s.now().Before(time.Unix(sess.CreatedAt, 0).Add(sessionRecordLifetime)) {
		return false
	}
	if aggregate(sess.Files) == statePending {
		if _, err := s.expire(ctx, sess, granted); err != nil {
			return false
		}
	}
	return true
}

func (s *Service) sweepUploads(ctx context.Context, granted scope) {
	abandoned := s.now().Add(-orphanAge)
	in := &s3.ListMultipartUploadsInput{
		Bucket:     aws.String(granted.bucket),
		MaxUploads: aws.Int32(sweptPerSweep),
	}
	if granted.prefix != "" {
		in.Prefix = aws.String(granted.prefix)
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
			_, _ = s.cfg.Objects.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(granted.bucket),
				Key:      upload.Key,
				UploadId: upload.UploadId,
			})
			if ctx.Err() != nil {
				return
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return
		}
		in.KeyMarker, in.UploadIdMarker = out.NextKeyMarker, out.NextUploadIdMarker
	}
}

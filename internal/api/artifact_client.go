package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func (c *Client) DownloadArtifact(ctx context.Context, id string, output io.Writer, maintenance bool) (domain.DeliveryArtifact, error) {
	var artifact domain.DeliveryArtifact
	if maintenance {
		data, err := c.Message(ctx, message.Request{Operation: "query.maintenance.software-artifact", Target: id})
		if err != nil {
			return artifact, err
		}
		if err := message.UnmarshalPayload(data, &artifact); err != nil {
			return artifact, err
		}
	} else {
		var err error
		artifact, err = Call[domain.DeliveryArtifact](ctx, c, "GET", Path("artifacts", id), nil)
		if err != nil {
			return artifact, err
		}
	}
	if artifact.ID != id || artifact.Size < 1 || artifact.Size > domain.MaxSoftwareBundleBytes || !domain.ValidHash(artifact.SHA256) {
		return artifact, domain.Fail(domain.Invalid, "artifact declaration is unavailable or exceeds the bounded download contract")
	}
	hash := sha256.New()
	for offset := int64(0); offset < artifact.Size; {
		var chunk domain.FileChunk
		if maintenance {
			data, err := c.Message(ctx, message.Request{Operation: "query.maintenance.software-artifact-chunk", Target: id,
				Query: map[string]string{"offset": strconv.FormatInt(offset, 10)}})
			if err != nil {
				return artifact, err
			}
			if err := message.UnmarshalPayload(data, &chunk); err != nil {
				return artifact, err
			}
		} else {
			var err error
			chunk, err = Call[domain.FileChunk](ctx, c, "GET", Path("artifacts", id, "chunks")+"?offset="+strconv.FormatInt(offset, 10), nil)
			if err != nil {
				return artifact, err
			}
		}
		if chunk.FileID != id || chunk.Offset != offset || int64(len(chunk.Data)) != min(int64(domain.FileChunkSize), artifact.Size-offset) ||
			domain.Hash(string(chunk.Data)) != chunk.SHA256 {
			return artifact, domain.Fail(domain.Prerequisite, "artifact chunk identity/offset/hash verification failed")
		}
		if _, err := io.MultiWriter(output, hash).Write(chunk.Data); err != nil {
			return artifact, err
		}
		offset += int64(len(chunk.Data))
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return artifact, domain.Fail(domain.Prerequisite, "artifact complete SHA-256 verification failed")
	}
	return artifact, nil
}

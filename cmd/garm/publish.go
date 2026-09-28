package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/garm-ai/garm/internal/compiler"
)

func newCataloguePublishCmd() *cobra.Command {
	var promptsRoot string
	cmd := &cobra.Command{
		Use:   "publish <catalogue.binpb> <s3://bucket/prefix/>",
		Short: "Upload a catalogue and the prompts it pins to an object store",
		Long: "publish puts every prompt an agent in the catalogue references at\n" +
			"<prefix>/prompts/<sha256>.md, and then the catalogue itself at\n" +
			"<prefix>/catalogue.binpb.\n\n" +
			"The order is the whole command. A daemon and a runner poll the\n" +
			"catalogue object and load whatever is there; a catalogue visible\n" +
			"before the files it pins is a generation that cannot be loaded, and\n" +
			"the reader has no way to tell that from a prompt that was tampered\n" +
			"with. So prompts go first, and the catalogue is the last byte\n" +
			"written.\n\n" +
			"Every prompt is read and hashed BEFORE anything is uploaded. A\n" +
			"missing or drifted prompt uploads nothing at all — not the other\n" +
			"prompts, and certainly not the catalogue.\n\n" +
			"Objects that already exist under a prompt's key are left alone. A\n" +
			"prompt is content-addressed, so one that is there is already the\n" +
			"right bytes.\n\n" +
			"The S3 client is the AWS SDK's default credential chain. Set\n" +
			"AWS_ENDPOINT_URL to publish to something other than AWS — SeaweedFS,\n" +
			"MinIO, a test double — and the client switches to path-style\n" +
			"addressing, which is what those stores serve.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCataloguePublish(cmd, args[0], args[1],
				resolvePromptsRoot(promptsRoot, "."))
		},
	}
	cmd.Flags().StringVar(&promptsRoot, "prompts-root", "",
		"Directory an agent's prompts.*.path resolves against (default: the working directory)")
	return cmd
}

// s3Dest is a parsed s3://bucket/prefix/ destination. Prefix is slash-joined
// and never has a leading or trailing slash, so path.Join below always
// produces the key a reader will ask for.
type s3Dest struct {
	Bucket string
	Prefix string
}

func parseS3Dest(raw string) (s3Dest, error) {
	rest, ok := strings.CutPrefix(raw, "s3://")
	if !ok {
		return s3Dest{}, fmt.Errorf("%q is not an s3 destination; it looks like "+
			"s3://bucket/prefix/", raw)
	}
	bucket, prefix, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return s3Dest{}, fmt.Errorf("%q names no bucket", raw)
	}
	return s3Dest{Bucket: bucket, Prefix: strings.Trim(prefix, "/")}, nil
}

// prompt is one verified prompt file: its declared hash, which is also its
// object key, and the bytes that hash to it.
type prompt struct {
	SHA256 string
	Body   []byte
}

func runCataloguePublish(cmd *cobra.Command, cataloguePath, dest, promptsRoot string) error {
	d, err := parseS3Dest(dest)
	if err != nil {
		return err
	}
	// Read once. The digest printed at the end describes exactly these
	// bytes — the ones actually uploaded — rather than a second read of the
	// same path, which could in principle see a different generation of the
	// file than the one just sent.
	body, err := os.ReadFile(cataloguePath)
	if err != nil {
		return fmt.Errorf("reading catalogue: %w", err)
	}
	fds, digest, err := parseCatalogue(cataloguePath, body)
	if err != nil {
		return err
	}

	// Everything is verified before anything is sent. See the command's Long.
	prompts, err := verifyPrompts(fds, promptsRoot)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	client, err := newS3Client(ctx)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	for _, p := range prompts {
		key := path.Join(d.Prefix, "prompts", p.SHA256+".md")
		exists, err := objectExists(ctx, client, d.Bucket, key)
		if err != nil {
			return fmt.Errorf("checking s3://%s/%s: %w", d.Bucket, key, err)
		}
		if exists {
			fmt.Fprintf(out, "skipped s3://%s/%s (already there)\n", d.Bucket, key)
			continue
		}
		if err := putObject(ctx, client, d.Bucket, key, p.Body, "text/markdown"); err != nil {
			return fmt.Errorf("uploading s3://%s/%s: %w; the catalogue was not written. "+
				"Any prompt this run already uploaded is harmless to leave in place — "+
				"each is addressed by its own sha256, so re-running publish re-verifies "+
				"the tree and only uploads what is still missing", d.Bucket, key, err)
		}
		fmt.Fprintf(out, "wrote s3://%s/%s\n", d.Bucket, key)
	}

	key := path.Join(d.Prefix, "catalogue.binpb")
	if err := putObject(ctx, client, d.Bucket, key, body, "application/octet-stream"); err != nil {
		return fmt.Errorf("uploading s3://%s/%s: %w", d.Bucket, key, err)
	}
	fmt.Fprintf(out, "wrote s3://%s/%s\n  %s\n", d.Bucket, key, digest)
	return nil
}

// verifyPrompts reads and hashes every prompt the catalogue references,
// refusing on the first problem and returning the verified set in a stable
// order, deduplicated by hash.
//
// Deduplicated because two agents sharing a system prompt reference one file
// twice, and uploading it twice would double the HEAD traffic for no benefit.
// Sorted because the upload order is asserted by a test and because a command
// whose output reorders between runs is one nobody can diff.
func verifyPrompts(fds []protoreflect.FileDescriptor, promptsRoot string) ([]prompt, error) {
	seen := map[string]bool{}
	var out []prompt
	for _, ref := range compiler.PromptRefs(fds) {
		if err := compiler.ValidatePromptPath(ref.Path); err != nil {
			return nil, fmt.Errorf("%s prompts[%q]: %q is not usable: %w",
				ref.Agent, ref.Key, ref.Path, err)
		}
		if err := compiler.ValidatePromptSHA256(ref.SHA256); err != nil {
			return nil, fmt.Errorf("%s prompts[%q]: %w", ref.Agent, ref.Key, err)
		}
		if seen[ref.SHA256] {
			continue
		}
		// The same resolved-path containment the linter applies (A2), not
		// just the lexical ValidatePromptPath above: a catalogue built
		// elsewhere may carry any path at all, and a symlink one level below
		// a clean-looking declared path can still lead outside the tree.
		// ContainedPromptPath is the one implementation of that check,
		// shared with internal/compiler's linter.
		resolved, err := compiler.ContainedPromptPath(promptsRoot, ref.Path)
		if err != nil {
			return nil, fmt.Errorf("%s prompts[%q]: %s is not under the prompts root %s: %v; "+
				"publish from the directory you built the catalogue in, or pass "+
				"--prompts-root", ref.Agent, ref.Key, ref.Path, promptsRoot, err)
		}
		body, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("%s prompts[%q]: %s is not under the prompts root %s: %v; "+
				"publish from the directory you built the catalogue in, or pass "+
				"--prompts-root", ref.Agent, ref.Key, ref.Path, promptsRoot, err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != ref.SHA256 {
			return nil, fmt.Errorf("%s prompts[%q]: %s hashes to %s and the catalogue "+
				"pins %s; publishing would put these bytes under that hash's key, "+
				"which is the one thing content addressing must never allow",
				ref.Agent, ref.Key, ref.Path, got, ref.SHA256)
		}
		seen[ref.SHA256] = true
		out = append(out, prompt{SHA256: ref.SHA256, Body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SHA256 < out[j].SHA256 })
	return out, nil
}

// newS3Client is the AWS SDK default chain, plus AWS_ENDPOINT_URL.
//
// Path-style addressing whenever an endpoint is set, because virtual-host
// addressing needs a wildcard DNS name per bucket and the stores this is
// pointed at locally — SeaweedFS in the compose file, a test double here —
// serve path-style only.
//
// Checksums are computed only when the operation requires them. The SDK's
// default adds a CRC32 trailer to every PutObject, which several
// S3-compatible stores reject outright; nothing here needs it, since every
// object is either content-addressed by its own sha256 or is the catalogue,
// whose digest the reader checks.
func newS3Client(ctx context.Context) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	}), nil
}

// objectExists answers the skip decision, and distinguishes "not there" from
// "could not tell". A store that is unreachable must fail the publish, not be
// read as an empty bucket and quietly re-upload everything.
func objectExists(ctx context.Context, c *s3.Client, bucket, key string) (bool, error) {
	_, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err == nil {
		return true, nil
	}
	var missing *s3types.NotFound
	if errors.As(err, &missing) {
		return false, nil
	}
	return false, err
}

func putObject(ctx context.Context, c *s3.Client, bucket, key string, body []byte, contentType string) error {
	// bytes.NewReader, not an io.Pipe or a file handle: the body must be
	// SEEKABLE so the SDK can sign it without buffering the whole object a
	// second time, and every object here is already in memory.
	_, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &bucket,
		Key:           &key,
		Body:          bytes.NewReader(body),
		ContentType:   &contentType,
		ContentLength: aws.Int64(int64(len(body))),
	})
	return err
}

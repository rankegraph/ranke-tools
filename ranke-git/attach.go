// package: main / ranke-git
// type:    logic + entrypoint
// job:     `ranke-git attach` — cites arbitrary content (a log, a report, an artifact) onto
// an already-archived commit
// limits:  the commit must already be archived; this adds source claims citing it and
// resolves the ref naming it, never walking git further (-> DESIGN.md)
package main

import (
	"cmp"
	"context"
	"crypto"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

// attach reuses gitPrefix (convert.go).

// edgeAttachedTo is attachment -> the commit it documents: a relation, since
// an attachment isn't an interpretation of the commit's content, it's
// evidence associated with the same point in time.
const edgeAttachedTo = "relation/attached_to"

// subtypeChars is the ADT's own rule (checkSubtype / R-FIELDS' field-name
// shape, shared): a leading [a-z0-9], then [a-z0-9_]*. Checked here so a bad
// --type fails before a network round trip, not inside the library.
var subtypeChars = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

func attachCmd(o *options) *cobra.Command {
	var commitSha, ref, gitBranch, gitTag string
	var s attachSpec
	c := &cobra.Command{
		Use:   "attach [flags] <file|dir>...",
		Short: "Attach arbitrary content (a log, a report, a release's assets) to an already-archived commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !subtypeChars.MatchString(s.kind) {
				return fmt.Errorf("attach: --type %q must match %s (lowercase letters, digits, underscore; no leading underscore)", s.kind, subtypeChars.String())
			}
			sha, err := attachCommit(o, commitSha, ref, gitBranch, gitTag)
			if err != nil {
				return err
			}
			as, err := s.attachments(cmd, args)
			if err != nil {
				return err
			}
			return runAttach(cmd, o, sha, as)
		},
	}
	c.Flags().StringVar(&commitSha, "commit", "", "the git sha of an already-archived commit")
	c.Flags().StringVar(&ref, "ref", "", "name that commit by a tag, branch, or commit in --clone instead, resolved git's own way")
	c.Flags().StringVar(&gitBranch, "git-branch", "", "name that commit by the tip of a git branch in --clone")
	c.Flags().StringVar(&gitTag, "git-tag", "", "name that commit by a git tag in --clone")
	c.Flags().StringVar(&s.kind, "type", "", "what this is, e.g. \"build_log\", \"test_report\", \"artifact\" — becomes source/git_<type> (required)")
	c.Flags().StringVar(&s.name, "name", "", "a title for this attachment (default: the file's name; required for stdin)")
	c.Flags().StringVar(&s.contentType, "content-type", "", "the attachment's media type (default: the file extension's, else what the bytes look like)")
	c.Flags().StringVar(&s.checksum, "checksum", "", "the artifact's published checksum, [<alg>:]<hex> — checked against the content, then recorded as the claim's checksum field")
	c.Flags().StringVar(&s.checksumFile, "checksum-file", "", "read that checksum from a shasum-style file")
	return c
}

// attachCommit is the sha the attachments cite: --commit as given, or the
// commit a ref names in --clone. Resolving one is the whole of attach's
// business with git — it still never walks the repo (-> DESIGN.md).
func attachCommit(o *options, commitSha, ref, branch, tag string) (string, error) {
	named := ref != "" || branch != "" || tag != ""
	switch {
	case commitSha != "" && named:
		return "", fmt.Errorf("attach: --commit and a ref both name the target commit — give one")
	case commitSha != "":
		return commitSha, nil
	case !named:
		return "", fmt.Errorf("attach: name the commit to attach to, with --commit, --ref, --git-tag, or --git-branch")
	}
	g, err := localRepo(o)
	if err != nil {
		return "", err
	}
	target, err := refTarget(ref, branch, tag)
	if err != nil {
		return "", fmt.Errorf("attach: %w", err)
	}
	return resolveRef(g, target)
}

// attachSpec is the command line as given, before any file is read.
type attachSpec struct {
	kind         string
	name         string
	contentType  string
	checksum     string
	checksumFile string
}

// attachment is one claim's worth: what to record, and the bytes it records.
type attachment struct {
	typ         string
	name        string
	contentType string
	checksum    string // "<alg>:<hex>", empty when none was given
	content     []byte
}

// attachments reads what the command line names: stdin where no path was
// given, otherwise every file the paths hold.
func (s attachSpec) attachments(cmd *cobra.Command, paths []string) ([]attachment, error) {
	if len(paths) == 0 {
		content, err := readStdin(cmd)
		if err != nil {
			return nil, err
		}
		if s.name == "" {
			return nil, fmt.Errorf("attach: --name is required for content read from stdin")
		}
		declared, err := s.declaredChecksum()
		if err != nil {
			return nil, err
		}
		a, err := s.attachment(s.name, content, declared)
		return []attachment{a}, err
	}
	files, err := walkPaths(cmd.ErrOrStderr(), paths)
	if err != nil {
		return nil, err
	}
	files, sidecars, err := takeSidecars(files)
	if err != nil {
		return nil, err
	}
	if len(files) > 1 && (s.name != "" || s.checksum != "" || s.checksumFile != "") {
		return nil, fmt.Errorf("attach: %d files, so --name, --checksum and --checksum-file cannot say which — attach one file at a time, or let each keep its own name", len(files))
	}
	declared, err := s.declaredChecksum()
	if err != nil {
		return nil, err
	}
	out := make([]attachment, 0, len(files))
	for _, f := range files {
		content, err := os.ReadFile(f.path)
		if err != nil {
			return nil, fmt.Errorf("attach: %w", err)
		}
		a, err := s.attachment(cmp.Or(s.name, f.name), content, cmp.Or(declared, sidecars[f.name]))
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// attachment builds one, verifying a checksum the caller resolved and
// sniffing the media type where --content-type left it open.
func (s attachSpec) attachment(name string, content []byte, checksum string) (attachment, error) {
	a := attachment{
		typ:         "source/" + gitPrefix + s.kind,
		name:        name,
		contentType: mediaType(s.contentType, name, content),
		content:     content,
	}
	if checksum == "" {
		return a, nil
	}
	verified, err := verifiedChecksum(checksum, content)
	if err != nil {
		return a, err
	}
	a.checksum = verified
	return a, nil
}

// declaredChecksum is --checksum, or the digest --checksum-file holds.
func (s attachSpec) declaredChecksum() (string, error) {
	if s.checksum != "" && s.checksumFile != "" {
		return "", fmt.Errorf("attach: --checksum and --checksum-file both name a digest — give one")
	}
	if s.checksumFile == "" {
		return s.checksum, nil
	}
	return readDigest(s.checksumFile)
}

// found is one file a positional path named, under the name it will carry.
type found struct{ path, name string }

// walkPaths expands the positional paths: a file stands for itself, a
// directory for every file beneath it, named by its path within.
func walkPaths(warn io.Writer, paths []string) ([]found, error) {
	var out []found
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("attach: %w", err)
		}
		if !info.IsDir() {
			out = append(out, found{path: p, name: filepath.Base(p)})
			continue
		}
		before := len(out)
		if err := filepath.WalkDir(p, func(sub string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir():
				return nil
			case d.Type()&fs.ModeSymlink != 0:
				// An attachment is the bytes it names, which a link is not.
				fmt.Fprintf(warn, ">> skipping symlink %s\n", sub)
				return nil
			}
			rel, err := filepath.Rel(p, sub)
			if err != nil {
				return err
			}
			out = append(out, found{path: sub, name: filepath.ToSlash(rel)})
			return nil
		}); err != nil {
			return nil, fmt.Errorf("attach: %s: %w", p, err)
		}
		if len(out) == before {
			return nil, fmt.Errorf("attach: %s holds no files", p)
		}
	}
	return out, nil
}

// takeSidecars hands each <file>.<alg> digest to the file beside it and drops
// it from the batch, so a release's published checksums land as fields.
func takeSidecars(files []found) ([]found, map[string]string, error) {
	named := make(map[string]bool, len(files))
	for _, f := range files {
		named[f.name] = true
	}
	sidecars := map[string]string{}
	keep := make([]found, 0, len(files))
	for _, f := range files {
		base, alg, ok := sidecarOf(f.name)
		if !ok || !named[base] {
			keep = append(keep, f)
			continue
		}
		digest, err := readDigest(f.path)
		if err != nil {
			return nil, nil, err
		}
		sidecars[base] = alg + ":" + digest
	}
	return keep, sidecars, nil
}

// sidecarOf reads "site.tar.gz.sha256" as the sha256 of "site.tar.gz".
func sidecarOf(name string) (base, alg string, ok bool) {
	alg = strings.TrimPrefix(filepath.Ext(name), ".")
	if _, known := checksumAlgs[alg]; !known {
		return "", "", false
	}
	return strings.TrimSuffix(name, "."+alg), alg, true
}

// readDigest reads shasum's own output: the digest, then the file it covers.
func readDigest(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("attach: %w", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("attach: %s holds no digest", path)
	}
	return fields[0], nil
}

// mediaType is --content-type where it was given, else the extension's type,
// else what the bytes themselves look like.
func mediaType(given, name string, content []byte) string {
	if given != "" {
		return given
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return baseMediaType(t)
	}
	return baseMediaType(http.DetectContentType(content))
}

// baseMediaType drops the parameters a sniffed type carries ("; charset=…").
func baseMediaType(t string) string {
	base, _, _ := strings.Cut(t, ";")
	return strings.TrimSpace(base)
}

// checksumAlgs are the digests attach verifies a --checksum against.
var checksumAlgs = map[string]func() hash.Hash{
	"md5":    md5.New,
	"sha1":   sha1.New,
	"sha256": sha256.New,
	"sha512": sha512.New,
}

// verifiedChecksum reads [<alg>:]<hex>, sha256 where the algorithm is left
// out, and returns it as "<alg>:<hex>" once the content hashes to it.
func verifiedChecksum(checksum string, content []byte) (string, error) {
	alg, digest := "sha256", strings.ToLower(strings.TrimSpace(checksum))
	if name, rest, found := strings.Cut(digest, ":"); found {
		alg, digest = name, rest
	}
	newHash, ok := checksumAlgs[alg]
	if !ok {
		known := slices.Sorted(maps.Keys(checksumAlgs))
		return "", fmt.Errorf("attach: --checksum algorithm %q is unknown — one of %s", alg, strings.Join(known, ", "))
	}
	h := newHash()
	h.Write(content)
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		return "", fmt.Errorf("attach: --checksum says %s:%s, the content is %s:%s", alg, digest, alg, got)
	}
	return alg + ":" + digest, nil
}

// readStdin takes the content a CI step pipes straight through, never
// writing the log down on the way.
func readStdin(cmd *cobra.Command) ([]byte, error) {
	content, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil, fmt.Errorf("attach: read stdin: %w", err)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("attach: no content — name a file or a directory, or pipe it in on stdin")
	}
	return content, nil
}

// runAttach finds the target commit's claim, builds every attachment citing
// it, and contributes them as one batch — no git repo involved, purely
// server- and content-facing.
func runAttach(cmd *cobra.Command, o *options, commitSha string, as []attachment) error {
	ctx := cmd.Context()
	s, err := connect(ctx, o)
	if err != nil {
		return err
	}
	target, err := findOne(ctx, s.client, o.branch, nodeCommit, gitShaField, commitSha)
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	if target == nil {
		return fmt.Errorf("attach: no archived commit with git_sha %q on branch %q", commitSha, o.branch)
	}

	out := cmd.OutOrStdout()
	u := ranke.NewMemoryUniverse()
	claims := make([]ranke.Claim, 0, len(as))
	for _, a := range as {
		claim, err := buildAttachment(ctx, u, s.contributor, s.signer, *target, a, time.Time{})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, ">> %s — %s, %d byte(s)%s\n", a.name, a.contentType, len(a.content), a.checksumNote())
		claims = append(claims, claim)
	}
	return contributeAndReport(ctx, s.client, u, o.branch, claims, out)
}

// checksumNote renders the verified checksum for the line attach prints.
func (a attachment) checksumNote() string {
	if a.checksum == "" {
		return ""
	}
	return ", " + a.checksum
}

// buildAttachment signs one source claim citing target via
// relation/attached_to: content external, so two identical attachments (the
// same log, reattached) share one content_hash. A zero at defaults to now.
func buildAttachment(
	ctx context.Context, u ranke.Universe, contributor ranke.Contributor, signer crypto.Signer,
	target reused, a attachment, at time.Time,
) (ranke.Claim, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	content := a.content
	id, err := ranke.HashContent(content)
	if err != nil {
		return nil, fmt.Errorf("attach: hash content: %w", err)
	}
	if err := u.PutContents(ctx, []ranke.ContentBlob{{Hash: id, Content: content}}); err != nil {
		return nil, fmt.Errorf("attach: store content: %w", err)
	}
	edge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: target.id, Type: edgeAttachedTo, RelationDirection: ranke.RelationTo,
	})
	if err != nil {
		return nil, fmt.Errorf("attach: attached_to edge: %w", err)
	}
	b := ranke.NewClaim(a.typ, contributor).
		WithExternalContent(id, uint64(len(content))).
		WithEncoding(a.contentType).
		WithField("name", a.name).
		WithCreatedAt(at).
		WithHeight(target.height + 1).
		WithEdges(edge)
	if a.checksum != "" {
		b = b.WithField("checksum", a.checksum)
	}
	claim, err := b.Sign(signer)
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	return claim, nil
}

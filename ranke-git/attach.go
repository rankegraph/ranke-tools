// package: main / ranke-git
// type:    logic + entrypoint
// job:     `ranke-git attach` — cites arbitrary content (a log, a report, an artifact) onto
// an already-archived commit
// limits:  the commit must already be archived; this only adds one source claim citing
// it, never re-walks git (-> DESIGN.md)
package main

import (
	"context"
	"crypto"
	"fmt"
	"io"
	"os"
	"regexp"
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
	var commitSha, kind, name, contentType, file string
	c := &cobra.Command{
		Use:   "attach",
		Short: "Attach arbitrary content (a log, a report, an artifact) to an already-archived commit",
		RunE: func(cmd *cobra.Command, args []string) error {
			if commitSha == "" {
				return fmt.Errorf("attach: --commit is required")
			}
			if name == "" {
				return fmt.Errorf("attach: --name is required")
			}
			if !subtypeChars.MatchString(kind) {
				return fmt.Errorf("attach: --type %q must match %s (lowercase letters, digits, underscore; no leading underscore)", kind, subtypeChars.String())
			}
			content, err := readAttachment(cmd, file)
			if err != nil {
				return err
			}
			return runAttach(cmd, o, commitSha, "source/"+gitPrefix+kind, name, contentType, content)
		},
	}
	c.Flags().StringVar(&commitSha, "commit", "", "the git sha of an already-archived commit (required)")
	c.Flags().StringVar(&kind, "type", "", "what this is, e.g. \"build_log\", \"test_report\", \"artifact_windows\" — becomes source/git_<type> (required)")
	c.Flags().StringVar(&name, "name", "", "a title for this attachment, e.g. \"release build log\" (required)")
	c.Flags().StringVar(&contentType, "content-type", "text/plain", "the attachment's media type")
	c.Flags().StringVar(&file, "file", "", "read content from this file instead of stdin")
	return c
}

// readAttachment reads content from file, or stdin when file is empty — the
// natural shape for a CI step piping its own log straight through.
func readAttachment(cmd *cobra.Command, file string) ([]byte, error) {
	if file != "" {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("attach: %w", err)
		}
		return content, nil
	}
	content, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil, fmt.Errorf("attach: read stdin: %w", err)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("attach: no content — pass --file or pipe it in on stdin")
	}
	return content, nil
}

// runAttach finds the target commit's claim, builds one attachment citing
// it, and contributes it — no git repo involved, purely server- and
// content-facing.
func runAttach(cmd *cobra.Command, o *options, commitSha, typ, name, contentType string, content []byte) error {
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

	u := ranke.NewMemoryUniverse()
	claim, err := buildAttachment(ctx, u, s.contributor, s.signer, *target, typ, name, contentType, content, time.Time{})
	if err != nil {
		return err
	}
	return contributeAndReport(ctx, s.client, u, o.branch, []ranke.Claim{claim}, cmd.OutOrStdout())
}

// buildAttachment signs one source claim citing target via
// relation/attached_to: content external, so two identical attachments (the
// same log, reattached) share one content_hash. A zero at defaults to now.
func buildAttachment(
	ctx context.Context, u ranke.Universe, contributor ranke.Contributor, signer crypto.Signer,
	target reused, typ, name, contentType string, content []byte, at time.Time,
) (ranke.Claim, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
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
	claim, err := ranke.NewClaim(typ, contributor).
		WithExternalContent(id, uint64(len(content))).
		WithEncoding(contentType).
		WithField("name", name).
		WithCreatedAt(at).
		WithHeight(target.height + 1).
		WithEdges(edge).
		Sign(signer)
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	return claim, nil
}

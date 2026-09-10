// package: main / ranke-git
// type:    entrypoint
// job:     `ranke-git identity register` — mints a real, persistent contributor,
// registers its root claim, and writes its signing key to disk — the one-time
// bootstrap a CI pipeline's own identity needs (unlike demo server's throwaway one)
// limits:  writes the key in cleartext; storing it safely (CI secret, vault) is the
// caller's job, not this command's
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/rankegraph/ranke-go"
)

func identityCmd(o *options) *cobra.Command {
	c := &cobra.Command{
		Use:   "identity",
		Short: "Manage contributor identities",
	}
	c.AddCommand(identityRegisterCmd(o))
	return c
}

func identityRegisterCmd(o *options) *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "register",
		Short: "Mint a contributor, register its root claim, and write its signing key to disk",
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				return fmt.Errorf("identity register: --out is required")
			}
			return runIdentityRegister(cmd, o, out)
		},
	}
	c.Flags().StringVar(&out, "out", "", "path to write the new ed25519 signing key, PEM (required; refuses to overwrite)")
	return c
}

// runIdentityRegister mints an ed25519 keypair, signs and contributes its
// root claim to o.branch, then writes the key — the same PKCS#8 PEM shape
// loadSigningKey reads back.
func runIdentityRegister(cmd *cobra.Command, o *options, out string) error {
	ctx := cmd.Context()
	if o.server == "" {
		return fmt.Errorf("identity register: --server is required")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("identity register: %s already exists — won't overwrite a signing key", out)
	}
	c := newClient(o.server, o.token, o.apiKey)
	if err := c.waitReady(ctx, 10*time.Second); err != nil {
		return fmt.Errorf("%s: %w", o.server, err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	encoded, err := ranke.EncodePublicKey(pub)
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	claim, err := ranke.NewClaim(ranke.NodeTypeContributor, nil).
		WithInlineContent(encoded).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	if err != nil {
		return err
	}
	if err := c.advanceClock(ctx, at); err != nil {
		return err
	}
	if _, err := c.contribute(ctx, ranke.NewMemoryUniverse(), o.branch, []ranke.Claim{claim}); err != nil {
		return fmt.Errorf("identity register: %w", err)
	}

	block, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("identity register: encode key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: block})
	if err := os.WriteFile(out, pemBytes, 0o600); err != nil {
		return fmt.Errorf("identity register: write %s: %w", out, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "registered contributor %s on branch %q\n", claim.ID(), o.branch)
	fmt.Fprintf(cmd.OutOrStdout(), "signing key written to %s (0600) — store it safely, then pass\n  --signing-key %s\n", out, out)
	fmt.Fprintf(cmd.OutOrStdout(), "the key finds this contributor on its own; --contributor-id %s names it where a branch holds two identities under one key\n", claim.ID())
	return nil
}

# ranke-tools — repo-level targets.
#
# One Go module, one subdirectory per tool (see README.md). No generation step here —
# unlike ranke-db, nothing in this repo is derived from a spec.

RANKE_GRAPH_REPO ?= https://github.com/rankegraph/ranke-graph
RANKE_GRAPH_REF  ?= main

# release-cycle.sh lives in ranke-graph and serves every consumer repo, so the git
# mechanics of a release are written once, there. Cached under bin/ (gitignored):
# fetched infrastructure, never vendored, so this repo cannot drift from it.
RANKE_GRAPH_RAW    := https://raw.githubusercontent.com/rankegraph/ranke-graph
RELEASE_CYCLER     := bin/release-cycle.sh
RELEASE_CYCLER_URL ?= $(RANKE_GRAPH_RAW)/$(RANKE_GRAPH_REF)/scripts/release-cycle.sh
PAPERS_DIR       := docs/papers
# Everything at the top of the paper repo is reference material and gets pulled;
# these are the exceptions (its own tooling). Dotdirs never match the glob.
PAPERS_SKIP      := scripts

# brokkr, installed on demand rather than assumed present. Cached under bin/ (already
# gitignored, already this repo's build-output directory) — the installer itself checks
# the latest release against what is already there and only downloads on a mismatch.
TOOLS_BIN         := bin/tools
BROKKR            := $(TOOLS_BIN)/brokkr
BROKKR_INSTALL_SH := https://raw.githubusercontent.com/flocko-motion/sindri/master/scripts/install-brokkr.sh

# One binary per top-level tool directory — add a name here when a new tool joins
# ranke-git, rather than hand-writing a second build recipe for it.
TOOLS := ranke-git

# Empty for a plain dev build (main.version stays "dev"); the release workflow
# sets it to stamp the tag into every tool's own main package.
LDFLAGS ?=

RANKE_DB_REPO ?= rankegraph/ranke-db

# The two dependencies that carry the graph contract: ranke-go builds and signs the
# claims, ranke-db's client sends them (see README.md).
RANKE_GO_MODULE ?= github.com/rankegraph/ranke-go
RANKE_DB_MODULE ?= github.com/rankegraph/ranke-db

.PHONY: all help build test vet fmt lint check tidy docs docs-clean upgrade check-clean-tree check-release-bump \
        release-gate release major minor patch breaking feature fix

.DEFAULT_GOAL := all

all: check ## Default: the whole quality gate

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Build every tool's binary into bin/ (LDFLAGS to stamp a version, e.g. LDFLAGS="-X main.version=v1.2.3")
	@for t in $(TOOLS); do \
		echo ">> build   → bin/$$t"; \
		go build -ldflags "$(LDFLAGS)" -o bin/$$t ./$$t || exit 1; \
	done

test: ## Test all packages; scope with make test/<pkg> (e.g. test/ranke-git)
	@echo ">> go test ./..."
	@go test ./...

test/%:
	@echo ">> go test -v ./$*/..."
	@go test -v ./$*/...

vet: ## go vet every package
	@go vet ./...

fmt: ## Check gofmt cleanliness (does not rewrite — see: gofmt -w .)
	@fmt=$$(gofmt -l $$(git ls-files '*.go')); \
	[ -z "$$fmt" ] || { echo "gofmt needed:"; echo "$$fmt"; exit 1; }

lint: ## Run brokkr lint — one already on PATH if there is one, else this repo's cached copy in bin/tools/ (the installer checks GitHub for a newer release every run, skipping the download itself when already current)
	@if command -v brokkr >/dev/null 2>&1; then bin=brokkr; \
	else \
		command -v curl >/dev/null 2>&1 || { echo "ERROR: brokkr not found and curl is not on PATH to install it"; exit 1; }; \
		curl -fsSL $(BROKKR_INSTALL_SH) | bash -s -- $(BROKKR); \
		bin=$(BROKKR); \
	fi; \
	"$$bin" lint

check: ## Whole-repo quality gate: build, vet, gofmt-check, test, lint
	@set -e; \
		$(MAKE) --no-print-directory build; \
		$(MAKE) --no-print-directory vet; \
		$(MAKE) --no-print-directory fmt; \
		$(MAKE) --no-print-directory test; \
		$(MAKE) --no-print-directory lint

tidy: ## go mod tidy
	@go mod tidy

docs: ## Pull the latest ranke-graph documents (papers, spec, glossary) into docs/papers/
	@echo ">> fetching ranke-graph documents into $(PAPERS_DIR)/"
	@tmp=$$(mktemp -d) && \
		git clone --depth 1 --branch $(RANKE_GRAPH_REF) $(RANKE_GRAPH_REPO) $$tmp >/dev/null 2>&1 && \
		rm -rf $(PAPERS_DIR) && mkdir -p $(PAPERS_DIR) && \
		for d in $$tmp/*/; do \
			name=$$(basename $$d); \
			case " $(PAPERS_SKIP) " in *" $$name "*) continue ;; esac; \
			cp -r $$d $(PAPERS_DIR)/; \
		done && \
		cp $$tmp/LICENSE $(PAPERS_DIR)/LICENSE 2>/dev/null || true; \
		rm -rf $$tmp; \
		echo ">> pulled $$(find $(PAPERS_DIR) -name '*.typ' | wc -l | tr -d ' ') document(s):"; \
		find $(PAPERS_DIR) -name '*.typ' | sort | sed 's|^|     |'

docs-clean: ## Remove the pulled paper references
	rm -rf $(PAPERS_DIR)

upgrade: ## Move every pin to its latest: ranke-go, ranke-db (client module and release binary, in step), and the cached release-cycle.sh
	@command -v curl >/dev/null 2>&1 || { echo "ERROR: curl not found"; exit 1; }
	@latest=$$(curl -fsSL https://api.github.com/repos/$(RANKE_DB_REPO)/releases/latest \
		| grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4); \
	[ -n "$$latest" ] || { echo "ERROR: couldn't resolve $(RANKE_DB_REPO)'s latest release"; exit 1; }; \
	current=$$(cat server/.rankedb-version 2>/dev/null || echo "none"); \
	if [ "$$current" = "$$latest" ]; then \
		echo ">> server/.rankedb-version already at $$latest"; \
	else \
		echo ">> server/.rankedb-version: $$current -> $$latest"; \
		echo "$$latest" > server/.rankedb-version; \
	fi
	@./server/install.sh
	@before=$$(go list -m -f '{{.Version}}' $(RANKE_GO_MODULE)); \
		go get $(RANKE_GO_MODULE)@latest && go mod tidy && \
		echo ">> $(RANKE_GO_MODULE): $$before -> $$(go list -m -f '{{.Version}}' $(RANKE_GO_MODULE))"
	@# The client module moves to the release the pinned server ships in: a client
	@# and the server it talks to have no business drifting apart.
	@pinned=$$(cat server/.rankedb-version); \
		before=$$(go list -m -f '{{.Version}}' $(RANKE_DB_MODULE)); \
		if [ "$$before" = "$$pinned" ]; then \
			echo ">> $(RANKE_DB_MODULE) already at $$pinned"; \
		else \
			go get $(RANKE_DB_MODULE)@$$pinned && go mod tidy && \
			echo ">> $(RANKE_DB_MODULE): $$before -> $$pinned"; \
		fi
	@rm -f $(RELEASE_CYCLER)
	@$(MAKE) $(RELEASE_CYCLER)

release-gate: check ## Run the pre-release quality gate without releasing

# A dirty tree and a missing bump word are free, instant checks; release-gate is
# not, so failing on them should not cost a build first.
check-clean-tree:
	@[ -z "$$(git status --porcelain)" ] || { echo "working tree is dirty — commit or stash before releasing" >&2; exit 1; }

check-release-bump:
	@[ -n "$(filter major minor patch breaking feature fix,$(MAKECMDGOALS))" ] || \
		{ echo "usage: make release <major|breaking | minor|feature | patch|fix>" >&2; exit 1; }

release: check-clean-tree check-release-bump release-gate $(RELEASE_CYCLER) ## Release every tool as one bundle, same version (bump: major|minor|patch, aliases breaking|feature|fix)
	@$(RELEASE_CYCLER) $(filter major minor patch breaking feature fix,$(MAKECMDGOALS))

$(RELEASE_CYCLER): ## Cache release-cycle.sh from ranke-graph (bin/ is gitignored — infra, never vendored)
	@mkdir -p $(dir $(RELEASE_CYCLER))
	@curl -fsSL $(RELEASE_CYCLER_URL) -o $(RELEASE_CYCLER)
	@chmod +x $(RELEASE_CYCLER)

major minor patch breaking feature fix:
	@:

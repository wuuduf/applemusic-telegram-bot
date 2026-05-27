# =============================================================================
# applemusic-telegram-bot — operator Makefile
#
# This file mostly exposes wrapper-manager admin shortcuts that are awkward to
# run by hand because they need an interactive terminal sharing a docker
# network with the running wrapper-manager container.
#
# Usage:
#
#     make help                                # list all targets
#     make wmcli-build                         # build the admin CLI image
#     make accounts                            # print wrapper-manager status
#     make login                               # prompt for one or more AppleIDs
#     make login-one USER=me@example.com       # log in a single AppleID
#     make login-batch FILE=accounts.tsv       # batch from a TSV file
#     make logout USERS="a@x.com b@y.com"      # log out one or more AppleIDs
#     make wmcli-shell                         # drop into the CLI container
#
# All targets build and run inside Docker, so you don't need Python on the
# host. They share the network namespace of the running wrapper-manager
# container (--network container:wrapper-manager) which means the CLI talks
# to it as `localhost:8080` regardless of the compose project name.
# =============================================================================

# ----- knobs ---------------------------------------------------------------

# Image tag for the admin CLI. Override if you want to push it somewhere.
WMCLI_IMAGE ?= applemusic-telegram-bot/wmcli:local

# Container that runs wrapper-manager. We attach via --network container:<name>
# so the CLI sees the same loopback as the manager and can dial localhost:8080.
WRAPPER_MANAGER_CONTAINER ?= wrapper-manager

# Address wmcli should dial inside its container. Don't change this unless
# you also change WRAPPER_MANAGER_CONTAINER and the network mode.
WRAPPER_MANAGER_ADDR ?= localhost:8080

# Resolved at use-time so you can override DOCKER for podman, nerdctl, etc.
DOCKER ?= docker

# Common run flags. -i lets us pipe stdin (e.g. for 2FA codes); -t adds a
# proper terminal for getpass + nicer prompts. Targets that don't need a
# TTY (CI scripts, etc.) can use the *-noninteractive variants.
RUN_FLAGS  := --rm -it --network container:$(WRAPPER_MANAGER_CONTAINER) \
              -e WRAPPER_MANAGER_ADDR=$(WRAPPER_MANAGER_ADDR)
RUN_FLAGS_NOTTY := --rm -i --network container:$(WRAPPER_MANAGER_CONTAINER) \
              -e WRAPPER_MANAGER_ADDR=$(WRAPPER_MANAGER_ADDR)

# ----- meta ----------------------------------------------------------------

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help (auto-generated from target docstrings).
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_.-]+:.*?## / \
		{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ----- wmcli image ---------------------------------------------------------

.PHONY: wmcli-build
wmcli-build: ## Build the wrapper-manager admin CLI image (idempotent).
	$(DOCKER) build -t $(WMCLI_IMAGE) -f tools/wrapper-manager-cli/Dockerfile .

.PHONY: wmcli-test
wmcli-test: ## Run the stdlib-only smoke test for the login state machine.
	python3 tools/wrapper-manager-cli/test_wmcli.py

# ----- runtime checks ------------------------------------------------------

.PHONY: _require-manager-running
_require-manager-running:
	@$(DOCKER) inspect -f '{{.State.Running}}' $(WRAPPER_MANAGER_CONTAINER) 2>/dev/null \
		| grep -q '^true$$' \
		|| { \
			echo "ERROR: container '$(WRAPPER_MANAGER_CONTAINER)' is not running." >&2; \
			echo "       Bring up the manager first:" >&2; \
			echo "         docker compose --profile manager up -d wrapper-manager" >&2; \
			exit 1; \
		}

# ----- account management --------------------------------------------------

.PHONY: accounts
accounts: wmcli-build _require-manager-running ## Show wrapper-manager status (ready / instances / regions).
	$(DOCKER) run $(RUN_FLAGS_NOTTY) $(WMCLI_IMAGE) status

.PHONY: login
login: wmcli-build _require-manager-running ## Interactive multi-account login (empty AppleID to finish).
	$(DOCKER) run $(RUN_FLAGS) $(WMCLI_IMAGE) login

.PHONY: login-one
login-one: wmcli-build _require-manager-running ## Log in one AppleID. Usage: make login-one USER=me@example.com [PASS=secret]
	@test -n "$(USER)" || { echo "Usage: make login-one USER=<AppleID> [PASS=<password>]"; exit 2; }
	$(DOCKER) run $(RUN_FLAGS) \
		$(if $(PASS),-e APPLE_PASSWORD=$(PASS),) \
		$(WMCLI_IMAGE) login -u $(USER)

.PHONY: login-batch
login-batch: wmcli-build _require-manager-running ## Batch login from a TSV file. Usage: make login-batch FILE=accounts.tsv
	@test -n "$(FILE)" || { echo "Usage: make login-batch FILE=<path/to/accounts.tsv>"; exit 2; }
	@test -f "$(FILE)" || { echo "ERROR: file not found: $(FILE)"; exit 2; }
	$(DOCKER) run $(RUN_FLAGS) \
		-v $(abspath $(FILE)):/work/accounts.tsv:ro \
		$(WMCLI_IMAGE) login -f /work/accounts.tsv

.PHONY: logout
logout: wmcli-build _require-manager-running ## Log out one or more AppleIDs. Usage: make logout USERS="a@x b@y"
	@test -n "$(USERS)" || { echo "Usage: make logout USERS=\"<AppleID1> [<AppleID2> ...]\""; exit 2; }
	$(DOCKER) run $(RUN_FLAGS_NOTTY) $(WMCLI_IMAGE) logout $(USERS)

# ----- escape hatch --------------------------------------------------------

.PHONY: wmcli-shell
wmcli-shell: wmcli-build _require-manager-running ## Drop into a shell inside the wmcli image (debugging).
	$(DOCKER) run $(RUN_FLAGS) --entrypoint /bin/sh $(WMCLI_IMAGE)

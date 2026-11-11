# judge-shim prelude (templates/judge-shim/judge_prelude.sh), sourced at the
# top of a judge-literal bundle's tests/test.sh, ahead of the upstream
# judge script's verbatim body. Makes the launch-selected Reward Kit judge
# (native_judge.py, this same directory) reachable and OAuth-ready.
#
# Fail closed: the judge backend is the only universal launch requirement.
# Model and effort are optional transport; the selected backend validates only
# the inputs it actually needs. No reward file is written on a configuration
# error, so Harbor records an exception rather than folding a false 0.0 into
# the mean.
export REWARDKIT_FORCE_OAUTH=1
export JUDGE_TIMEOUT_SEC="${JUDGE_TIMEOUT_SEC:-600}"

# Antigravity subscription auth: agy has no headless login. Seed its OAuth
# cache from AGY_AUTH_JSON_PATH / AGY_AUTH_JSON without printing the credential.
AGY_HOME="${AGY_HOME:-$HOME/.gemini/antigravity-cli}"
AGY_TOKEN_PATH="$AGY_HOME/antigravity-oauth-token"
if [ ! -s "$AGY_TOKEN_PATH" ]; then
  mkdir -p "$AGY_HOME"
  if [ -n "${AGY_AUTH_JSON_PATH:-}" ] && [ -s "$AGY_AUTH_JSON_PATH" ]; then
    cp "$AGY_AUTH_JSON_PATH" "$AGY_TOKEN_PATH"
  elif [ -n "${AGY_AUTH_JSON:-}" ]; then
    printf '%s' "$AGY_AUTH_JSON" > "$AGY_TOKEN_PATH"
  fi
  [ -f "$AGY_TOKEN_PATH" ] && chmod 600 "$AGY_TOKEN_PATH"
fi

if [ -z "${REWARDKIT_JUDGE:-}" ]; then
  echo "[judge-shim] NOT SCORED: REWARDKIT_JUDGE (launch-selected judge backend) is required" >&2
  exit 1
fi
if [ "$REWARDKIT_JUDGE" = "agy" ]; then
  # Effort-tiered model IDs (gemini-*-low|medium|high) already carry their
  # effort in the name -- agy hard-rejects an explicit --effort flag next to
  # one ("invalid model selection"), which is exactly why validate-real
  # deliberately clears AGY_MODEL_EFFORT to "" for these models (see its agy
  # judge branch). rewardkit's own AntigravityCLI backend already treats an
  # empty/unset AGY_MODEL_EFFORT as "don't pass --effort" -- it is only
  # required for a bare (non-tiered) gemini-* model, where there is no other
  # way to supply the tier. This check used to require AGY_MODEL_EFFORT be
  # set to low|medium|high unconditionally whenever REWARDKIT_JUDGE=agy,
  # which made every tiered-model run on every judge-shim bundle (aa-lcr,
  # clbench, financeagent, ineqmath, kramabench, omnimath, seal0) fail
  # closed with "AGY_MODEL_EFFORT must be low|medium|high for agy" even
  # though the launch was correct (ineqmath oracle, gemini-3.7-flash-medium,
  # 2026-09-05). Mirrors validate-real's own case split and the identical
  # fix already applied to conversation-rubric et al. (commit b01611d,
  # 2026-09-05; see test_agy_effort_tiered_models.py).
  case "$REWARDKIT_MODEL" in
    *-low|*-medium|*-high)
      tier="${REWARDKIT_MODEL##*-}"
      if [ -n "${AGY_MODEL_EFFORT:-}" ] && [ "$AGY_MODEL_EFFORT" != "$tier" ]; then
        echo "[judge-shim] NOT SCORED: AGY_MODEL_EFFORT must be empty or match tier $tier for $REWARDKIT_MODEL" >&2
        exit 1
      fi
      ;;
    gemini-*)
      case "${AGY_MODEL_EFFORT:-}" in
        low|medium|high) ;;
        "")
          echo "[judge-shim] NOT SCORED: explicit AGY_MODEL_EFFORT required for agy" >&2
          exit 1
          ;;
        *)
          echo "[judge-shim] NOT SCORED: invalid AGY_MODEL_EFFORT=$AGY_MODEL_EFFORT" >&2
          exit 1
          ;;
      esac
      ;;
    *)
      # Non-Gemini agy judge models (claude/gpt-oss) reject an effort tier
      # entirely; only a mistakenly non-empty value is an error here.
      if [ -n "${AGY_MODEL_EFFORT:-}" ]; then
        echo "[judge-shim] NOT SCORED: invalid AGY_MODEL_EFFORT=$AGY_MODEL_EFFORT for $REWARDKIT_MODEL (no effort tier accepted)" >&2
        exit 1
      fi
      ;;
  esac
  if [ ! -s "$AGY_TOKEN_PATH" ]; then
    echo "[judge-shim] NOT SCORED: agy OAuth cache is missing (seed AGY_AUTH_JSON / AGY_AUTH_JSON_PATH)" >&2
    exit 1
  fi
fi
python3 -c "import rewardkit" || { echo "[judge-shim] NOT SCORED: rewardkit not importable in the grader image" >&2; exit 1; }

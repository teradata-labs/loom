"""r1-as-judge probe: rerun the Phase 3 judge-accuracy probe with deepseek-r1.

Identical 20 (problem, weak-solution) pairs and identical prompt to the
llama3.1 probe (rzJudgeProbePrompt in leveling_reasoning_live_test.go), so the
two judges are directly comparable. Pivotal question: is judge quality a
model-selection problem (reasoning judge ~= oracle) or a scale problem?

Paths are configurable so the JSONL is regenerable from any checkout. Each has a
CLI flag, an environment variable, and a default expressed relative to this
file's location in the repo (`<repo>/docs/experiments/`):

  --experiments-dir / LOOM_PROBE_EXPERIMENTS_DIR
      Directory holding the probe inputs (judge_probe.jsonl,
      reasoning_arms.jsonl). Default: this script's own directory.
  --out / LOOM_PROBE_OUT
      Output JSONL. Default: <experiments-dir>/r1_judge_probe.jsonl — i.e. a
      rerun overwrites the committed copy in place.
  --calib-dir / LOOM_PROBE_CALIB_DIR
      Directory holding the harness helper module `ollama_client` (which carries
      the cloud guard). This lived in the original run's scratchpad and is not
      committed, so the default below only resolves on the machine that ran the
      experiment; point it at a checkout of those helpers elsewhere.
  --judge / LOOM_PROBE_JUDGE
      Judge model. Default: deepseek-r1:latest (as measured).
"""
import argparse, json, os, re, sys, time
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_DEFAULT_CALIB = ("/private/tmp/claude-501/-Users-josh-schoen-repos-loom--claude-worktrees-"
                  "affectionate-colden/3d7bbe33-d920-4f5c-aec0-3a31ff447f0d/scratchpad/calib")

_p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
_p.add_argument("--experiments-dir", default=os.environ.get("LOOM_PROBE_EXPERIMENTS_DIR", str(_HERE)))
_p.add_argument("--out", default=os.environ.get("LOOM_PROBE_OUT"))
_p.add_argument("--calib-dir", default=os.environ.get("LOOM_PROBE_CALIB_DIR", _DEFAULT_CALIB))
_p.add_argument("--judge", default=os.environ.get("LOOM_PROBE_JUDGE", "deepseek-r1:latest"))
_args = _p.parse_args()

EXPERIMENTS = Path(_args.experiments_dir)
OUT = _args.out or str(EXPERIMENTS / "r1_judge_probe.jsonl")
JUDGE = _args.judge

sys.path.insert(0, _args.calib_dir)
from ollama_client import chat  # noqa: E402  (carries the cloud guard)

PROMPT = """Expression: %s

Below is a submitted solution for that expression. Check the arithmetic and decide whether the submitted final answer is correct.

Submitted solution:
<<<
%s
>>>

Reply with a single JSON object and nothing else, in this form:
{"verdict": "correct"} or {"verdict": "incorrect"}"""

VERDICT_RE = re.compile(r'(?i)"verdict"\s*:\s*"?(correct|incorrect)"?')

# The 20 probe pairs are the indices in judge_probe.jsonl; the raw weak
# solutions live in reasoning_arms.jsonl arm 1 attempts[0].output.
probe_rows = [json.loads(l) for l in open(EXPERIMENTS / "judge_probe.jsonl")]
arm1 = {r["index"]: r for r in
        (json.loads(l) for l in open(EXPERIMENTS / "reasoning_arms.jsonl"))
        if r["arm"] == "1-llama2-off"}

agree = fp = fn = unparsed = 0
results = []
for row in probe_rows:
    i = row["index"]
    raw_solution = arm1[i]["attempts"][0]["output"]
    truth_correct = row["weak_correct"]
    t0 = time.time()
    resp = chat(JUDGE, PROMPT % (row["expression"], raw_solution),
                temperature=0.1, num_predict=3072, seed=0)
    dt = time.time() - t0
    text = resp.get("text", "") or ""
    m = VERDICT_RE.search(text)
    verdict = m.group(1).lower() if m else "UNPARSED"
    says_right = verdict == "correct"
    if verdict == "UNPARSED":
        unparsed += 1
        agrees = False
    else:
        agrees = says_right == truth_correct
        agree += agrees
        if says_right and not truth_correct:
            fp += 1
        if not says_right and truth_correct:
            fn += 1
    results.append({"index": i, "expression": row["expression"],
                    "weak_correct": truth_correct, "verdict": verdict,
                    "agrees": agrees, "seconds": round(dt, 2)})
    print(f"i={i:<3} weak_correct={truth_correct!s:<5} r1_verdict={verdict:<10} "
          f"agrees={agrees!s:<5} {dt:.1f}s", flush=True)

with open(OUT, "w") as f:
    for r in results:
        f.write(json.dumps(r) + "\n")

n = len(probe_rows)
lat = sorted(r["seconds"] for r in results)
print(f"\nRESULT r1-as-judge: agreement {agree}/{n} ({100*agree/n:.0f}%)  "
      f"false-pass={fp} false-fail={fn} unparsed={unparsed}  "
      f"latency med={lat[n//2]:.1f}s max={lat[-1]:.1f}s", flush=True)
print("(llama3.1 baseline on identical pairs: 7/20 (35%), false-pass=13, false-fail=0)", flush=True)

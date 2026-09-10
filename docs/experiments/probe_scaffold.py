"""C2 scaffolding probe: does FORCING decomposition fix llama2's arithmetic?

Same 30 problems as the Phase 3 arms (arith_chain L11 indices 0-29). Baseline
from arm 1: 12/30 correct. Calibration showed llama2 is ~0-10% wrong with
1-digit multipliers but 60% wrong with 2-digit ones, and its failures are
single-op slips inside correct procedures. If forced partial-product
decomposition collapses the wrong-rate, C2 (proactive scaffolding, NO signal
needed) beats C3 (reactive escalation, signal-bound) on this task class.

Two scaffold depths, S1 light / S2 heavy, 30 problems each, llama2 only.

Paths are configurable so the JSONL is regenerable from any checkout. Each has a
CLI flag, an environment variable, and a default expressed relative to this
file's location in the repo (`<repo>/docs/experiments/`):

  --out / LOOM_PROBE_OUT
      Output JSONL. Default: <this script's directory>/scaffold_probe.jsonl —
      i.e. a rerun overwrites the committed copy in place.
  --calib-dir / LOOM_PROBE_CALIB_DIR
      Directory holding the harness helper modules `ollama_client` (which
      carries the cloud guard), `parse` and `generators`. These lived in the
      original run's scratchpad and are not committed, so the default below only
      resolves on the machine that ran the experiment; point it at a checkout of
      those helpers elsewhere.
  --model / LOOM_PROBE_MODEL
      Model under test. Default: llama2:latest (as measured).
"""
import argparse, json, os, sys, time
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_DEFAULT_CALIB = ("/private/tmp/claude-501/-Users-josh-schoen-repos-loom--claude-worktrees-"
                  "affectionate-colden/3d7bbe33-d920-4f5c-aec0-3a31ff447f0d/scratchpad/calib")

_p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
_p.add_argument("--out", default=os.environ.get("LOOM_PROBE_OUT"))
_p.add_argument("--calib-dir", default=os.environ.get("LOOM_PROBE_CALIB_DIR", _DEFAULT_CALIB))
_p.add_argument("--model", default=os.environ.get("LOOM_PROBE_MODEL", "llama2:latest"))
_args = _p.parse_args()

OUT = _args.out or str(_HERE / "scaffold_probe.jsonl")
MODEL = _args.model

sys.path.insert(0, _args.calib_dir)
from ollama_client import chat          # noqa: E402
from parse import parse_answer          # noqa: E402
from generators import gen_arith_chain  # noqa: E402

S1 = """Evaluate this expression using standard order of operations:

{expr}

Rules you must follow exactly:
1. Compute the parenthesized sum first, on its own line.
2. Compute the multiplication next, on its own line.
3. Compute the subtraction last, on its own line.
4. Re-check each line's arithmetic before moving to the next.

Then reply with a single JSON object and nothing else, in this form:
{{"answer": <integer>}}"""

S2 = """Evaluate this expression step by step:

{expr}

Follow these steps exactly, one line each:
1. SUM: add the two numbers in parentheses. Write "SUM = <value>".
2. Split the multiplier into tens and ones. Write "TENS = <multiplier tens digit> * 10, ONES = <ones digit>".
3. PART1: multiply SUM by the tens part. Write "PART1 = SUM * <tens> = <value>".
4. PART2: multiply SUM by the ones digit. Write "PART2 = SUM * <ones> = <value>".
5. PRODUCT: add PART1 and PART2. Write "PRODUCT = <value>".
6. RESULT: subtract the final number from PRODUCT. Write "RESULT = <value>".
7. CHECK: verify RESULT + <final number> equals PRODUCT. If not, redo steps 3-6.

Then reply with a single JSON object and nothing else, in this form:
{{"answer": <integer>}}"""

problems = [gen_arith_chain(11, i) for i in range(30)]

results = []
for name, tmpl in (("S1-light", S1), ("S2-partial-products", S2)):
    correct = contract = 0
    lats = []
    for p in problems:
        t0 = time.time()
        resp = chat(MODEL, tmpl.format(expr=p.meta["expression"]), temperature=0.1,
                    num_predict=1024, seed=0)
        dt = time.time() - t0
        lats.append(dt)
        ans, mode, _detail = parse_answer(resp.get("text", "") or "")
        ok = ans == p.answer
        correct += ok
        contract += mode == "json_strict"
        results.append({"scaffold": name, "index": p.index, "expr": p.meta["expression"],
                        "truth": p.answer, "got": ans, "correct": ok,
                        "parse_mode": mode, "seconds": round(dt, 2)})
        print(f"[{name}] i={p.index:<3} truth={p.answer:<6} got={ans!s:<8} "
              f"correct={ok!s:<5} {dt:.1f}s", flush=True)
    lats.sort()
    n = len(lats)
    print(f"\nRESULT {name}: {correct}/{n} correct ({100*correct/n:.0f}%)  "
          f"strict-contract={contract}/{n}  lat med={lats[n//2]:.1f}s max={lats[-1]:.1f}s "
          f"(baseline arm 1: 12/30 = 40%)\n", flush=True)

with open(OUT, "w") as f:
    for r in results:
        f.write(json.dumps(r) + "\n")

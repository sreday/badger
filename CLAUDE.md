# Working in this repo

- **Deploy only with `tools/deploy.sh`.** Never build a binary and copy it to the server by hand, and never deploy uncommitted or unpushed work. Order: commit, `git pull --rebase`, push `main`, then `tools/deploy.sh`. Two machines deploy to the same server; a hand-copied build from one silently removes the other's unpushed features from the live site (this happened on 2026-11-16).
- If the script stops with "the live build is a commit that was never pushed", do not use `--replace` to get past it. The missing work is on another machine: say so and wait for it to be pushed.
- This repo is public: no private business details, real names or call content in code, tests, docs or commit messages.
- Each tool lives in its own folder (`snout/`, `snug/`, `sniff/`, `snoop/`, `snap/`, `snitch/`); the original printer at the repo root stays untouched.

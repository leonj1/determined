# Jose's Personal Conventions

Full OKF bundle: https://github.com/leonj1/open-doc-format/tree/master/personal-knowledge

Clone for local access:
  gh repo clone leonj1/open-doc-format ~/src/open-doc-format

Fetch any doc on demand:
  gh api repos/leonj1/open-doc-format/contents/personal-knowledge/<path> --jq ".content" | base64 -d

---

## Code Structure (read first)
~/src/open-doc-format/personal-knowledge/conventions/code-structure.md
Key: I/O interfaces + Fake impls, constructor DI, typed arguments, typed objects over primitives, return values not mutations, no static, Result types not exceptions, <700-line classes, <30-line functions, ≤2 indentations

## Project Structure
~/src/open-doc-format/personal-knowledge/conventions/project-structure.md
Key: src/services, src/clients, src/models, src/routes, tests/ at top level

## Naming
~/src/open-doc-format/personal-knowledge/conventions/naming.md
Key: nouns for classes, verbs for functions, depth = name length

## Git Commits
~/src/open-doc-format/personal-knowledge/conventions/git-commits.md
Key: FEAT/BUG/CHORE prefixes, feature branches, main/master default

## Deployment
~/src/open-doc-format/personal-knowledge/deployment/
Key: Vercel for static, Railway for backends, Dockerfiles, docker-compose, Makefile

---

Before writing code:
1. Read ~/src/open-doc-format/personal-knowledge/conventions/code-structure.md
2. Read ~/src/open-doc-format/personal-knowledge/conventions/project-structure.md
3. Apply all rules listed above


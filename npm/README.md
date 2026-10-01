# heall

A self-healing CI agent. When a build goes red, heall finds the commit that
broke it, asks a model for a fix, proves the fix in a sandbox, and opens a
draft pull request with the evidence. When it cannot prove a fix, it stops
and says why.

```
cd your-project
npx heall init
npx heall run --good <last green commit> --dry-run
```

Or install it once and drop the `npx`:

```
npm install -g heall
heall init
```

The first run downloads the heall program for your machine (Linux or macOS)
and checks it against its published checksum. heall also needs git, Docker
and Python 3.10 or newer, and a [Groq](https://console.groq.com) API key;
`heall init` checks all of that and tells you what is missing.

Documentation: https://github.com/TheCodeHeist-Coder/heall

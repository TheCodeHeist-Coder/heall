#!/usr/bin/env node
// Generates the heall demo repository: a small JavaScript library ("shopkit")
// with a green main branch and three scenario branches of 120 commits each.
//
//   node demo/generate.mjs [--out demo/out] [--verify key|all]
//
// Output:
//   <out>/repo            the git repository
//   <out>/logs/<name>.log test output at the tip of each scenario branch
//   <out>/scenarios.json  ground truth: good, bad and culprit commits
//
// The history is deterministic (fixed authors, dates and content), so commit
// hashes are the same on every run. Each scenario is checked as it is built;
// --verify all checks every commit instead of only the ones that matter.

import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = args.indexOf(name);
  return i === -1 ? fallback : args[i + 1];
};
const OUT = path.resolve(flag("--out", path.join(HERE, "out")));
const VERIFY = flag("--verify", "key");
if (VERIFY !== "key" && VERIFY !== "all") {
  fail(`--verify must be "key" or "all", got "${VERIFY}"`);
}
const REPO = path.join(OUT, "repo");
const MANIFEST = path.join(OUT, "scenarios.json");
const BRANCH_COMMITS = 120;

// Ignore the user's git config so hashes do not depend on the machine.
const ENV = {
  ...process.env,
  GIT_CONFIG_GLOBAL: "/dev/null",
  GIT_CONFIG_SYSTEM: "/dev/null",
  GIT_TERMINAL_PROMPT: "0",
};
delete ENV.NODE_OPTIONS;

const AUTHORS = {
  asha: ["Asha Rao", "asha@shopkit.example"],
  ravi: ["Ravi Menon", "ravi@shopkit.example"],
  lena: ["Lena Fischer", "lena@shopkit.example"],
  tomas: ["Tomas Silva", "tomas@shopkit.example"],
};

function fail(message) {
  console.error(`generate: ${message}`);
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Files that exist on main

// The demo repository's build step. Loading every module in one process
// catches syntax errors and broken imports, and starts one Node process
// instead of one per file.
const CHECK_SCRIPT = `// Loads every module under src/ so that a syntax error or a broken import
// fails fast, before the tests run.
import { readdirSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const files = readdirSync("src", { recursive: true })
  .filter((f) => f.endsWith(".js"))
  .sort();

for (const f of files) {
  try {
    await import(pathToFileURL(path.resolve("src", f)));
  } catch (err) {
    console.error(\`\${f}: \${err.message}\`);
    process.exit(1);
  }
}
console.log(\`\${files.length} modules ok\`);
`;

// --test-isolation=none runs every test file in one process. The suite has
// no shared state, and it makes a test run about three times faster.
const TEST_ARGS = ["--test", "--test-isolation=none", "--test-reporter=tap"];

const HEALL_YAML = `version: 1

# Exits non-zero when a commit has a syntax error, so bisect can skip it.
build_cmd: ["node", "scripts/check.mjs"]
test_cmd: ["node", "--test", "--test-isolation=none", "--test-reporter=tap"]
test_one_cmd: ["node", "--test", "--test-isolation=none", "--test-reporter=tap", "--test-name-pattern=^{{test_re}}$"]

# A patch may only touch allowed paths, and never protected ones.
allow:
  - "src/**"
protect:
  - "test/**"
  - "**/*.test.js"
  - "scripts/**"
  - "package.json"
  - ".heall.yaml"

# No line a patch adds may match these: code that could make a test pass
# without fixing anything, by noticing it is being tested or by quitting.
forbid_added:
  - "NODE_TEST_CONTEXT"
  - "process\\\\.exit\\\\("
  - "\\\\.stack\\\\b"

sandbox:
  mode: docker
  image: node:24-alpine
  timeout_seconds: 60

locate:
  workers: 6

reproduce:
  runs: 8

heal:
  max_attempts: 3
  model: openai/gpt-oss-120b
`;

const PACKAGE_JSON = `{
  "name": "shopkit",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "node --test"
  }
}
`;

const CI_YAML = `name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 24
      - run: node scripts/check.mjs
      - run: node --test
`;

const TEST_HEADER = `import { test } from "node:test";
import assert from "node:assert/strict";
`;

const PAGINATE = `// Number of pages needed to show \`total\` items, \`size\` per page.
export function pageCount(total, size) {
  return Math.ceil(total / size);
}

// Items on a 1-based page.
export function paginate(items, page, size) {
  const start = (page - 1) * size;
  return items.slice(start, start + size);
}
`;

const paginateTest = (names, extra = "") => `${TEST_HEADER}import { ${names} } from "../src/paginate.js";

const items = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

test("pageCount rounds up a partial page", () => {
  assert.equal(pageCount(10, 3), 4);
});

test("paginate returns a full page", () => {
  assert.deepEqual(paginate(items, 2, 3), [4, 5, 6]);
});

test("paginate returns the last partial page", () => {
  assert.deepEqual(paginate(items, 4, 3), [10]);
});

test("paginate returns nothing past the end", () => {
  assert.deepEqual(paginate(items, 5, 3), []);
});
${extra}`;

const FORMAT = `// Formats an amount in cents as a price, e.g. 1250 -> "$12.50".
export function formatPrice(cents) {
  return \`$\${(cents / 100).toFixed(2)}\`;
}
`;

const FORMAT_TEST = `${TEST_HEADER}import { formatPrice } from "../src/format.js";

test("formatPrice shows dollars and cents", () => {
  assert.equal(formatPrice(1250), "$12.50");
});

test("formatPrice pads whole amounts", () => {
  assert.equal(formatPrice(500), "$5.00");
});
`;

const CART = `import { formatPrice } from "./format.js";

// Total of a cart in cents.
export function cartTotal(items) {
  let total = 0;
  for (const item of items) total += item.cents * item.qty;
  return total;
}

// Label shown next to each cart line.
export function priceLabel(cents) {
  return formatPrice(cents);
}
`;

const CART_TEST = `${TEST_HEADER}import { cartTotal, priceLabel } from "../src/cart.js";
import { formatPrice } from "../src/format.js";

test("cartTotal multiplies price by quantity", () => {
  const items = [
    { cents: 250, qty: 2 },
    { cents: 999, qty: 1 },
  ];
  assert.equal(cartTotal(items), 1499);
});

test("priceLabel uses the shared formatter", () => {
  assert.equal(priceLabel(1999), formatPrice(1999));
  assert.equal(priceLabel(1999), "$19.99");
});
`;

const RETRY = `const BASE_MS = 100;
const CAP_MS = 2000;

// Delay before retry number \`attempt\` (0-based): doubles each time, capped.
export function backoffDelay(attempt) {
  return Math.min(BASE_MS * 2 ** attempt, CAP_MS);
}
`;

const retryTest = (names, extra = "") => `${TEST_HEADER}import { ${names} } from "../src/retry.js";

test("backoffDelay doubles each attempt", () => {
  assert.equal(backoffDelay(0), 100);
  assert.equal(backoffDelay(3), 800);
});

test("backoffDelay is capped", () => {
  assert.equal(backoffDelay(10), 2000);
});
${extra}`;

const BASE_API = [
  ["paginate.pageCount", "`paginate.pageCount(total, size)`: Pages needed for `total` items."],
  ["paginate.paginate", "`paginate.paginate(items, page, size)`: Items on a 1-based page."],
  ["format.formatPrice", "`format.formatPrice(cents)`: Price string such as `$12.50`."],
  ["cart.cartTotal", "`cart.cartTotal(items)`: Cart total in cents."],
  ["cart.priceLabel", "`cart.priceLabel(cents)`: Label shown next to a cart line."],
  ["retry.backoffDelay", "`retry.backoffDelay(attempt)`: Capped exponential backoff in ms."],
];

// ---------------------------------------------------------------------------
// Filler: small helpers added over time. Each becomes three commits (the
// function with a test, an edge-case test, a README entry), which gives every
// branch a realistic history that keeps the suite green.
// [module, name, params, body, doc, [test, expression, expected] x2]

const CATALOG = [
  ["text", "capitalize", "s", "return s.charAt(0).toUpperCase() + s.slice(1);", "Uppercases the first character.",
    ["uppercases the first letter", 'text.capitalize("hello")', '"Hello"'],
    ["leaves an empty string alone", 'text.capitalize("")', '""']],
  ["text", "truncate", "s, max", 'return s.length <= max ? s : s.slice(0, max - 3) + "...";', "Shortens a string to `max` characters, ending in an ellipsis.",
    ["shortens long strings", 'text.truncate("hello world", 8)', '"hello..."'],
    ["keeps short strings", 'text.truncate("hi", 8)', '"hi"']],
  ["text", "kebabCase", "s", 'return s.trim().toLowerCase().split(/\\s+/).join("-");', "Lowercases words and joins them with dashes.",
    ["joins words with dashes", 'text.kebabCase("Hello Big World")', '"hello-big-world"'],
    ["trims surrounding space", 'text.kebabCase("  one  ")', '"one"']],
  ["text", "camelCase", "s", 'return s\n  .trim()\n  .toLowerCase()\n  .split(/\\s+/)\n  .map((w, i) => (i === 0 ? w : w.charAt(0).toUpperCase() + w.slice(1)))\n  .join("");', "Joins words in camelCase.",
    ["joins words", 'text.camelCase("hello big world")', '"helloBigWorld"'],
    ["lowercases a single word", 'text.camelCase("One")', '"one"']],
  ["text", "countWords", "s", 'const t = s.trim();\nreturn t === "" ? 0 : t.split(/\\s+/).length;', "Counts whitespace-separated words.",
    ["counts words across extra spaces", 'text.countWords("a b  c")', "3"],
    ["counts nothing in blank text", 'text.countWords("   ")', "0"]],
  ["text", "reverse", "s", 'return [...s].reverse().join("");', "Reverses a string.",
    ["reverses characters", 'text.reverse("abc")', '"cba"'],
    ["handles an empty string", 'text.reverse("")', '""']],
  ["text", "isPalindrome", "s", 'const c = s.toLowerCase().replace(/[^a-z0-9]/g, "");\nreturn c === [...c].reverse().join("");', "True when the text reads the same both ways, ignoring case and spacing.",
    ["ignores case and spaces", 'text.isPalindrome("Never odd or even")', "true"],
    ["rejects other text", 'text.isPalindrome("shopkit")', "false"]],
  ["text", "padLeft", "s, width, ch", "return String(s).padStart(width, ch);", "Pads a value on the left to `width`.",
    ["pads to the width", 'text.padLeft("7", 3, "0")', '"007"'],
    ["leaves longer values alone", 'text.padLeft("1234", 3, "0")', '"1234"']],
  ["text", "initials", "name", 'return name\n  .trim()\n  .split(/\\s+/)\n  .map((w) => w.charAt(0).toUpperCase())\n  .join("");', "Upper-case initials of a name.",
    ["takes the first letter of each word", 'text.initials("ada lovelace")', '"AL"'],
    ["handles a single name", 'text.initials("Grace")', '"G"']],
  ["text", "countOccurrences", "s, sub", 'return sub === "" ? 0 : s.split(sub).length - 1;', "Counts non-overlapping occurrences of `sub`.",
    ["counts matches", 'text.countOccurrences("banana", "an")', "2"],
    ["counts nothing for an empty needle", 'text.countOccurrences("abc", "")', "0"]],

  ["array", "chunk", "items, size", "const out = [];\nfor (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));\nreturn out;", "Splits a list into groups of `size`.",
    ["splits into groups", "array.chunk([1, 2, 3, 4, 5], 2)", "[[1, 2], [3, 4], [5]]"],
    ["returns nothing for an empty list", "array.chunk([], 3)", "[]"]],
  ["array", "unique", "items", "return [...new Set(items)];", "Removes duplicates, keeping first occurrences.",
    ["drops duplicates", "array.unique([1, 2, 2, 3, 1])", "[1, 2, 3]"],
    ["handles an empty list", "array.unique([])", "[]"]],
  ["array", "last", "items", "return items[items.length - 1];", "Last item of a list.",
    ["returns the final item", "array.last([1, 2, 3])", "3"],
    ["returns undefined for an empty list", "array.last([])", "undefined"]],
  ["array", "sum", "nums", "return nums.reduce((a, b) => a + b, 0);", "Adds up a list of numbers.",
    ["adds numbers", "array.sum([1, 2, 3])", "6"],
    ["is zero for an empty list", "array.sum([])", "0"]],
  ["array", "compact", "items", "return items.filter(Boolean);", "Removes falsy values.",
    ["drops falsy values", 'array.compact([0, 1, "", 2, null])', "[1, 2]"],
    ["handles an empty list", "array.compact([])", "[]"]],
  ["array", "flatten", "items", "return items.flat();", "Flattens one level of nesting.",
    ["flattens nested lists", "array.flatten([[1], [2, 3]])", "[1, 2, 3]"],
    ["flattens only one level", "array.flatten([1, [2, [3]]])", "[1, 2, [3]]"]],
  ["array", "zip", "a, b", "return a.slice(0, Math.min(a.length, b.length)).map((x, i) => [x, b[i]]);", "Pairs up items of two lists.",
    ["pairs items", 'array.zip([1, 2], ["a", "b"])', '[[1, "a"], [2, "b"]]'],
    ["stops at the shorter list", 'array.zip([1, 2, 3], ["a"])', '[[1, "a"]]']],
  ["array", "range", "start, end", "const out = [];\nfor (let i = start; i < end; i++) out.push(i);\nreturn out;", "Integers from `start` up to but not including `end`.",
    ["counts up to the end", "array.range(0, 3)", "[0, 1, 2]"],
    ["is empty when start equals end", "array.range(3, 3)", "[]"]],
  ["array", "partition", "items, pred", "const yes = [];\nconst no = [];\nfor (const x of items) (pred(x) ? yes : no).push(x);\nreturn [yes, no];", "Splits a list by a predicate.",
    ["splits by the predicate", "array.partition([1, 2, 3, 4], (n) => n % 2 === 0)", "[[2, 4], [1, 3]]"],
    ["handles an empty list", "array.partition([], Boolean)", "[[], []]"]],
  ["array", "groupBy", "items, key", "const out = {};\nfor (const x of items) (out[key(x)] ??= []).push(x);\nreturn out;", "Groups items by the result of `key`.",
    ["groups by key", 'array.groupBy(["a", "bb", "c"], (s) => s.length)', '{ 1: ["a", "c"], 2: ["bb"] }'],
    ["handles an empty list", "array.groupBy([], String)", "{}"]],

  ["number", "clamp", "n, min, max", "return Math.min(Math.max(n, min), max);", "Limits a number to a range.",
    ["caps at the maximum", "number.clamp(15, 0, 10)", "10"],
    ["raises to the minimum", "number.clamp(-2, 0, 10)", "0"]],
  ["number", "roundTo", "n, places", "const f = 10 ** places;\nreturn Math.round(n * f) / f;", "Rounds to a number of decimal places.",
    ["rounds to two places", "number.roundTo(3.14159, 2)", "3.14"],
    ["rounds halves up", "number.roundTo(2.5, 0)", "3"]],
  ["number", "isEven", "n", "return n % 2 === 0;", "True for even integers.",
    ["accepts even numbers", "number.isEven(4)", "true"],
    ["rejects odd numbers", "number.isEven(7)", "false"]],
  ["number", "percent", "part, total", "return total === 0 ? 0 : Math.round((part / total) * 100);", "Whole-number percentage of `part` in `total`.",
    ["computes a percentage", "number.percent(1, 4)", "25"],
    ["is zero when the total is zero", "number.percent(3, 0)", "0"]],
  ["number", "average", "nums", "return nums.length === 0 ? 0 : nums.reduce((a, b) => a + b, 0) / nums.length;", "Mean of a list of numbers.",
    ["computes the mean", "number.average([2, 4, 6])", "4"],
    ["is zero for an empty list", "number.average([])", "0"]],
  ["number", "inRange", "n, min, max", "return n >= min && n <= max;", "True when `n` is between `min` and `max` inclusive.",
    ["accepts values inside", "number.inRange(5, 1, 10)", "true"],
    ["rejects values outside", "number.inRange(11, 1, 10)", "false"]],
  ["number", "gcd", "a, b", "while (b !== 0) [a, b] = [b, a % b];\nreturn Math.abs(a);", "Greatest common divisor.",
    ["finds the common divisor", "number.gcd(12, 18)", "6"],
    ["handles zero", "number.gcd(7, 0)", "7"]],
  ["number", "median", "nums", "if (nums.length === 0) return 0;\nconst s = [...nums].sort((a, b) => a - b);\nconst mid = Math.floor(s.length / 2);\nreturn s.length % 2 === 1 ? s[mid] : (s[mid - 1] + s[mid]) / 2;", "Median of a list of numbers.",
    ["picks the middle value", "number.median([3, 1, 2])", "2"],
    ["averages the two middle values", "number.median([1, 2, 3, 4])", "2.5"]],

  ["object", "pick", "obj, keys", "const out = {};\nfor (const k of keys) if (k in obj) out[k] = obj[k];\nreturn out;", "Copies only the listed keys.",
    ["keeps listed keys", 'object.pick({ a: 1, b: 2, c: 3 }, ["a", "c"])', "{ a: 1, c: 3 }"],
    ["ignores missing keys", 'object.pick({ a: 1 }, ["z"])', "{}"]],
  ["object", "omit", "obj, keys", "const out = { ...obj };\nfor (const k of keys) delete out[k];\nreturn out;", "Copies everything except the listed keys.",
    ["drops listed keys", 'object.omit({ a: 1, b: 2 }, ["b"])', "{ a: 1 }"],
    ["handles an empty object", 'object.omit({}, ["a"])', "{}"]],
  ["object", "isEmpty", "obj", "return Object.keys(obj).length === 0;", "True when an object has no own keys.",
    ["accepts an empty object", "object.isEmpty({})", "true"],
    ["rejects an object with keys", "object.isEmpty({ a: 1 })", "false"]],
  ["object", "invert", "obj", "const out = {};\nfor (const [k, v] of Object.entries(obj)) out[v] = k;\nreturn out;", "Swaps keys and values.",
    ["swaps keys and values", 'object.invert({ a: "x", b: "y" })', '{ x: "a", y: "b" }'],
    ["handles an empty object", "object.invert({})", "{}"]],
  ["object", "mapValues", "obj, fn", "const out = {};\nfor (const [k, v] of Object.entries(obj)) out[k] = fn(v);\nreturn out;", "Applies `fn` to every value.",
    ["transforms each value", "object.mapValues({ a: 1, b: 2 }, (n) => n * 2)", "{ a: 2, b: 4 }"],
    ["handles an empty object", "object.mapValues({}, String)", "{}"]],
  ["object", "hasPath", "obj, path", 'let cur = obj;\nfor (const k of path.split(".")) {\n  if (cur == null || !(k in Object(cur))) return false;\n  cur = cur[k];\n}\nreturn true;', "True when a dotted path exists.",
    ["finds a nested key", 'object.hasPath({ a: { b: 1 } }, "a.b")', "true"],
    ["reports a missing key", 'object.hasPath({ a: {} }, "a.b")', "false"]],
  ["object", "getPath", "obj, path, fallback", 'let cur = obj;\nfor (const k of path.split(".")) {\n  if (cur == null) return fallback;\n  cur = cur[k];\n}\nreturn cur === undefined ? fallback : cur;', "Reads a dotted path, or returns `fallback`.",
    ["reads a nested value", 'object.getPath({ a: { b: 5 } }, "a.b", 0)', "5"],
    ["falls back when the path is missing", 'object.getPath({}, "a.b", "none")', '"none"']],
  ["object", "countKeys", "obj", "return Object.keys(obj).length;", "Number of own keys.",
    ["counts keys", "object.countKeys({ a: 1, b: 2 })", "2"],
    ["is zero for an empty object", "object.countKeys({})", "0"]],
].map(([mod, name, params, body, doc, t1, t2]) => ({ mod, name, params, body, doc, t1, t2 }));

const MODULE_TITLES = {
  text: "String helpers.",
  array: "List helpers.",
  number: "Number helpers.",
  object: "Plain-object helpers.",
};

// ---------------------------------------------------------------------------
// Scenarios. `at` is the 0-based position of a scripted commit on its branch.

const SCENARIOS = [
  {
    name: "off-by-one",
    branch: "bug/off-by-one",
    seed: 11,
    expected: "fixed",
    escalateStage: null,
    target: { name: "paginate returns a full page", file: "test/paginate.test.js" },
    summary:
      "A refactor makes paginate() drop the last item of a page. Two commits in the range have syntax errors. The obvious fix (changing bounds()) breaks other tests; the right fix is one line in paginate().",
    scripted: [
      {
        at: 16,
        tag: "unbuildable",
        author: "tomas",
        message: ["refactor(cart): total the cart with reduce"],
        apply: () =>
          write("src/cart.js", CART.replace(
            "  let total = 0;\n  for (const item of items) total += item.cents * item.qty;\n  return total;",
            "  return items.reduce((total, item) => total + item.cents * item.qty, 0;",
          )),
      },
      {
        at: 17,
        author: "tomas",
        message: ["fix(cart): close the reduce call"],
        apply: () =>
          write("src/cart.js", CART.replace(
            "  let total = 0;\n  for (const item of items) total += item.cents * item.qty;\n  return total;",
            "  return items.reduce((total, item) => total + item.cents * item.qty, 0);",
          )),
      },
      {
        at: 52,
        tag: "culprit",
        author: "ravi",
        message: [
          "refactor(paginate): share page bounds, add pageLabel",
          "paginate() and the new pageLabel() both need the indexes of a page,\nso compute them once in bounds().",
        ],
        apply: (state) => {
          write("src/paginate.js", PAGINATE_BOUNDS);
          write("test/paginate.test.js", paginateTest("bounds, pageCount, pageLabel, paginate", PAGINATE_BOUNDS_TESTS));
          state.api.push(
            ["paginate.bounds", "`paginate.bounds(page, size)`: First and last index of a page."],
            ["paginate.pageLabel", "`paginate.pageLabel(total, page, size)`: Label such as `items 4-6 of 10`."],
          );
          write("README.md", readme(state));
        },
      },
      {
        at: 67,
        tag: "unbuildable",
        author: "lena",
        message: ["refactor(retry): name the growth factor"],
        apply: () => write("src/retry.js", RETRY_FACTOR.replace(/\}\n$/, "")),
      },
      {
        at: 68,
        author: "lena",
        message: ["fix(retry): restore the closing brace"],
        apply: () => write("src/retry.js", RETRY_FACTOR),
      },
      {
        at: 95,
        author: "asha",
        message: ["feat(paginate): add hasNextPage"],
        apply: (state) => {
          write("src/paginate.js", PAGINATE_BOUNDS + HAS_NEXT_PAGE);
          write(
            "test/paginate.test.js",
            paginateTest("bounds, hasNextPage, pageCount, pageLabel, paginate", PAGINATE_BOUNDS_TESTS + HAS_NEXT_PAGE_TESTS),
          );
          state.api.push(["paginate.hasNextPage", "`paginate.hasNextPage(total, page, size)`: True when another page follows."]);
          write("README.md", readme(state));
        },
      },
    ],
  },
  {
    name: "outdated-test",
    branch: "change/price-format",
    seed: 23,
    expected: "escalated",
    escalateStage: "heal",
    target: { name: "priceLabel uses the shared formatter", file: "test/cart.test.js" },
    summary:
      "formatPrice() changed format on purpose (a documented product decision), but one test in another file still asserts the old format. The two tests now contradict each other, so no source-only patch can pass both; the test must be updated by a person.",
    scripted: [
      {
        at: 61,
        tag: "culprit",
        author: "lena",
        message: [
          "feat(format)!: show prices with a currency code",
          "Product decision PRICING-142: every price is now shown as \"12.50 USD\"\ninstead of \"$12.50\", so the storefront can add more currencies.\n\nBREAKING CHANGE: formatPrice() no longer returns a \"$\" prefix. This\napplies everywhere formatPrice() is used, including cart labels.",
        ],
        apply: (state) => {
          write("src/format.js", FORMAT_USD);
          write("test/format.test.js", FORMAT_USD_TEST);
          const entry = state.api.find(([key]) => key === "format.formatPrice");
          entry[1] = "`format.formatPrice(cents)`: Price string such as `12.50 USD`.";
          write("README.md", readme(state));
          state.changelog.push(
            "- **Breaking:** prices are shown with a currency code (`12.50 USD`) instead of a `$` prefix, everywhere `formatPrice` is used (PRICING-142).",
          );
          write("CHANGELOG.md", changelog(state));
        },
      },
    ],
  },
  {
    name: "flaky",
    branch: "flaky/retry-jitter",
    seed: 37,
    expected: "escalated",
    escalateStage: "reproduce",
    target: { name: "jitteredDelay stays close to the base delay", file: "test/retry.test.js" },
    flaky: true,
    summary:
      "A new test depends on Math.random() and fails about half the time on the same commit. No commit can be blamed, so heall should stop at Reproduce.",
    scripted: [
      {
        at: 66,
        tag: "culprit",
        author: "tomas",
        message: [
          "feat(retry): add jitter to the backoff delay",
          "Spread retries out so clients do not all retry at the same moment.",
        ],
        apply: (state) => {
          write("src/retry.js", RETRY + JITTER);
          write("test/retry.test.js", retryTest("backoffDelay, jitteredDelay", JITTER_TEST));
          state.api.push(["retry.jitteredDelay", "`retry.jitteredDelay(attempt)`: Backoff delay with random jitter."]);
          write("README.md", readme(state));
        },
      },
    ],
  },
];

// bounds() returns an inclusive `end`, which pageLabel() needs. paginate()
// passes it to slice(), whose end is exclusive: that is the bug.
const PAGINATE_BOUNDS = `// Number of pages needed to show \`total\` items, \`size\` per page.
export function pageCount(total, size) {
  return Math.ceil(total / size);
}

// Indexes of a 1-based page: \`start\` is its first item, \`end\` its last.
export function bounds(page, size) {
  const start = (page - 1) * size;
  return { start, end: start + size - 1 };
}

// Items on a 1-based page.
export function paginate(items, page, size) {
  const { start, end } = bounds(page, size);
  return items.slice(start, end);
}

// Label for a page, e.g. "items 4-6 of 10".
export function pageLabel(total, page, size) {
  const { start, end } = bounds(page, size);
  return \`items \${start + 1}-\${Math.min(end + 1, total)} of \${total}\`;
}
`;

const PAGINATE_BOUNDS_TESTS = `
test("bounds gives the first and last index of a page", () => {
  assert.deepEqual(bounds(2, 3), { start: 3, end: 5 });
});

test("pageLabel describes a full page", () => {
  assert.equal(pageLabel(10, 2, 3), "items 4-6 of 10");
});

test("pageLabel clamps the last page", () => {
  assert.equal(pageLabel(10, 4, 3), "items 10-10 of 10");
});
`;

const HAS_NEXT_PAGE = `
// True when there is another page after this one.
export function hasNextPage(total, page, size) {
  return bounds(page, size).end < total - 1;
}
`;

const HAS_NEXT_PAGE_TESTS = `
test("hasNextPage is true before the last page", () => {
  assert.equal(hasNextPage(10, 3, 3), true);
});

test("hasNextPage is false on the last page", () => {
  assert.equal(hasNextPage(10, 4, 3), false);
});
`;

const RETRY_FACTOR = `const BASE_MS = 100;
const CAP_MS = 2000;
const FACTOR = 2;

// Delay before retry number \`attempt\` (0-based): doubles each time, capped.
export function backoffDelay(attempt) {
  return Math.min(BASE_MS * FACTOR ** attempt, CAP_MS);
}
`;

const FORMAT_USD = `// Formats an amount in cents as a price with its currency code,
// e.g. 1250 -> "12.50 USD".
export function formatPrice(cents) {
  return \`\${(cents / 100).toFixed(2)} USD\`;
}
`;

const FORMAT_USD_TEST = `${TEST_HEADER}import { formatPrice } from "../src/format.js";

test("formatPrice shows the amount and currency code", () => {
  assert.equal(formatPrice(1250), "12.50 USD");
  assert.equal(formatPrice(1999), "19.99 USD");
});

test("formatPrice pads whole amounts", () => {
  assert.equal(formatPrice(500), "5.00 USD");
});
`;

const JITTER = `
// Backoff delay with random jitter of up to 10% either way.
export function jitteredDelay(attempt, random = Math.random) {
  return Math.round(backoffDelay(attempt) * (0.9 + random() * 0.2));
}
`;

// Passes only when the random jitter lands within 5%: about half of all runs.
const JITTER_TEST = `
test("jitteredDelay stays close to the base delay", () => {
  const delay = jitteredDelay(3);
  assert.ok(Math.abs(delay - 800) <= 40, \`delay was \${delay}\`);
});
`;

// ---------------------------------------------------------------------------
// Rendering and git plumbing

function mulberry32(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const indent = (code) => code.split("\n").map((line) => `  ${line}`).join("\n");

function moduleSource(mod, fns) {
  const body = fns
    .map((f) => `// ${f.doc}\nexport function ${f.name}(${f.params}) {\n${indent(f.body)}\n}\n`)
    .join("\n");
  return `// ${MODULE_TITLES[mod]}\n\n${body}`;
}

function moduleTest(mod, tests) {
  const body = tests
    .map(([title, expr, want]) => `test("${title}", () => {\n  assert.deepEqual(${expr}, ${want});\n});\n`)
    .join("\n");
  return `${TEST_HEADER}import * as ${mod} from "../src/${mod}.js";\n\n${body}`;
}

function readme(state) {
  return `# shopkit

Small, dependency-free helpers for a storefront: pagination, price
formatting, cart maths, retry timing and a few general utilities.

## Development

Requires Node 24. Run the tests with \`node --test\`.

## API

${state.api.map(([, line]) => `- ${line}`).join("\n")}
`;
}

function changelog(state) {
  return `# Changelog\n\n## Unreleased\n\n${state.changelog.join("\n")}\n`;
}

function freshState() {
  return {
    mods: Object.fromEntries(Object.keys(MODULE_TITLES).map((m) => [m, { fns: [], tests: [] }])),
    api: BASE_API.map((entry) => [...entry]),
    changelog: ["- Initial helpers: pagination, price formatting, cart totals, retry backoff."],
    lastFeat: null,
  };
}

function write(rel, content) {
  const file = path.join(REPO, rel);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, content);
}

function git(...argv) {
  return execFileSync("git", argv, { cwd: REPO, env: ENV, encoding: "utf8" }).trim();
}

let clock = Date.parse("2026-07-06T09:00:00Z");
const clockRand = mulberry32(7);

function commit(message, authorKey) {
  clock += (2 + Math.floor(clockRand() * 8)) * 3600_000;
  const date = new Date(clock).toISOString();
  const [name, email] = AUTHORS[authorKey];
  const env = {
    ...ENV,
    GIT_AUTHOR_NAME: name,
    GIT_AUTHOR_EMAIL: email,
    GIT_AUTHOR_DATE: date,
    GIT_COMMITTER_NAME: name,
    GIT_COMMITTER_EMAIL: email,
    GIT_COMMITTER_DATE: date,
  };
  execFileSync("git", ["add", "-A"], { cwd: REPO, env });
  const parts = (Array.isArray(message) ? message : [message]).flatMap((m) => ["-m", m]);
  execFileSync("git", ["commit", "-q", ...parts], { cwd: REPO, env });
  return git("rev-parse", "HEAD");
}

// ---------------------------------------------------------------------------
// Checks, run with the same commands heall will use

const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function builds() {
  return spawnSync("node", ["scripts/check.mjs"], { cwd: REPO, env: ENV, encoding: "utf8" }).status === 0;
}

function runTests(...extra) {
  const r = spawnSync("node", [...TEST_ARGS, ...extra], {
    cwd: REPO,
    env: ENV,
    encoding: "utf8",
  });
  return { ok: r.status === 0, out: `${r.stdout}${r.stderr}` };
}

// "pass", "fail", or "missing" when no test of that name ran.
function targetVerdict(name) {
  const { out } = runTests(`--test-name-pattern=${escapeRe(name)}`);
  const m = out.match(new RegExp(`^\\s*(not ok|ok) \\d+ - ${escapeRe(name)}\\s*$`, "m"));
  if (!m) return "missing";
  return m[1] === "ok" ? "pass" : "fail";
}

function check(condition, where, message) {
  if (!condition) fail(`${where}: ${message}`);
}

// Confirms a commit behaves the way its scenario says it should.
function verifyCommit(scn, i, op, failingFrom) {
  const where = `${scn.branch} #${i} "${op.subject}"`;
  if (op.tag === "unbuildable") {
    check(!builds(), where, "should not build, but it does");
    return;
  }
  check(builds(), where, "does not build");
  if (i < failingFrom) {
    const run = runTests();
    check(run.ok, where, `suite should pass before the culprit\n${run.out}`);
    return;
  }
  const rest = runTests(`--test-skip-pattern=${escapeRe(scn.target.name)}`);
  check(rest.ok, where, `tests other than the target should pass\n${rest.out}`);
  if (!scn.flaky) {
    const verdict = targetVerdict(scn.target.name);
    check(verdict === "fail", where, `target test should fail, got ${verdict}`);
  }
}

// ---------------------------------------------------------------------------
// Building the history

function fillerOps(rand, count) {
  const fns = [...CATALOG];
  for (let i = fns.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1));
    [fns[i], fns[j]] = [fns[j], fns[i]];
  }
  const authors = Object.keys(AUTHORS);
  const pick = () => authors[Math.floor(rand() * authors.length)];

  const feat = (f) => ({
    subject: `feat(${f.mod}): add ${f.name}`,
    author: pick(),
    apply: (state) => {
      const mod = state.mods[f.mod];
      mod.fns.push(f);
      mod.tests.push([`${f.name} ${f.t1[0]}`, f.t1[1], f.t1[2]]);
      write(`src/${f.mod}.js`, moduleSource(f.mod, mod.fns));
      write(`test/${f.mod}.test.js`, moduleTest(f.mod, mod.tests));
      state.lastFeat = f;
    },
  });
  const edge = (f) => ({
    subject: `test(${f.mod}): cover ${f.name} edge case`,
    author: pick(),
    apply: (state) => {
      const mod = state.mods[f.mod];
      mod.tests.push([`${f.name} ${f.t2[0]}`, f.t2[1], f.t2[2]]);
      write(`test/${f.mod}.test.js`, moduleTest(f.mod, mod.tests));
    },
  });
  const docs = (f) => ({
    subject: `docs: document ${f.mod}.${f.name}`,
    author: pick(),
    apply: (state) => {
      state.api.push([`${f.mod}.${f.name}`, `\`${f.mod}.${f.name}(${f.params})\`: ${f.doc}`]);
      write("README.md", readme(state));
    },
  });
  const note = () => ({
    subject: "docs(changelog): note recent additions",
    author: pick(),
    apply: (state) => {
      const f = state.lastFeat;
      state.changelog.push(`- Added \`${f.mod}.${f.name}\`.`);
      write("CHANGELOG.md", changelog(state));
    },
  });

  const ops = [];
  fns.forEach((f, i) => {
    ops.push(feat(f));
    if (i >= 1) ops.push(edge(fns[i - 1]));
    if (i >= 2) ops.push(docs(fns[i - 2]));
  });
  ops.push(edge(fns.at(-1)), docs(fns.at(-2)), docs(fns.at(-1)));

  const notes = count - ops.length;
  check(notes >= 0, "filler", `branch needs ${count} filler commits but the catalog yields ${ops.length}`);
  const step = ops.length / (notes + 1);
  for (let k = notes; k >= 1; k--) ops.splice(Math.round(k * step), 0, note());
  return ops;
}

function buildMain() {
  fs.mkdirSync(REPO, { recursive: true });
  git("init", "-q", "-b", "main");
  const state = freshState();

  write("package.json", PACKAGE_JSON);
  write(".gitignore", "node_modules/\n");
  write("README.md", readme({ api: [] }).replace(/\n## API\n\n\n$/, ""));
  commit("chore: set up the project", "asha");

  write("src/paginate.js", PAGINATE);
  write("test/paginate.test.js", paginateTest("pageCount, paginate"));
  commit("feat(paginate): add pageCount and paginate", "asha");

  write("src/format.js", FORMAT);
  write("test/format.test.js", FORMAT_TEST);
  commit("feat(format): add formatPrice", "lena");

  write("src/cart.js", CART);
  write("test/cart.test.js", CART_TEST);
  commit("feat(cart): add cartTotal and priceLabel", "lena");

  write("src/retry.js", RETRY);
  write("test/retry.test.js", retryTest("backoffDelay"));
  commit("feat(retry): add capped exponential backoff", "tomas");

  write("README.md", readme(state));
  write("CHANGELOG.md", changelog(state));
  commit("docs: add API list and changelog", "ravi");

  write("scripts/check.mjs", CHECK_SCRIPT);
  write(".github/workflows/ci.yml", CI_YAML);
  write(".heall.yaml", HEALL_YAML);
  const sha = commit("ci: run the tests on every push", "ravi");

  check(builds(), "main", "does not build");
  const run = runTests();
  check(run.ok, "main", `suite should pass\n${run.out}`);
  return sha;
}

function buildScenario(scn, good) {
  git("checkout", "-q", "-b", scn.branch, "main");
  const state = freshState();
  const scripted = [...scn.scripted].sort((a, b) => a.at - b.at);
  const ops = fillerOps(mulberry32(scn.seed), BRANCH_COMMITS - scripted.length);
  for (const s of scripted) {
    ops.splice(s.at, 0, { ...s, subject: s.message[0] });
  }
  check(ops.length === BRANCH_COMMITS, scn.branch, `has ${ops.length} commits, want ${BRANCH_COMMITS}`);

  const failingFrom = ops.findIndex((op) => op.tag === "culprit");
  const key = new Set([0, failingFrom - 1, failingFrom, ops.length - 1]);
  ops.forEach((op, i) => {
    if (op.tag === "unbuildable") [i, i + 1].forEach((n) => key.add(n));
  });

  let culprit = null;
  const unbuildable = [];
  ops.forEach((op, i) => {
    op.apply(state);
    const sha = commit(op.message ?? op.subject, op.author);
    if (op.tag === "culprit") culprit = sha;
    if (op.tag === "unbuildable") unbuildable.push(sha);
    if (VERIFY === "all" || key.has(i)) verifyCommit(scn, i, op, failingFrom);
    if (VERIFY === "all" && (i + 1) % 20 === 0) console.log(`  ${scn.branch}: ${i + 1}/${ops.length} verified`);
  });

  // Capture a failing run at the tip, the way a CI log would show it.
  let log = "";
  let fails = 0;
  const tries = scn.flaky ? 40 : 1;
  for (let n = 0; n < tries; n++) {
    const verdict = targetVerdict(scn.target.name);
    check(verdict !== "missing", scn.branch, "target test did not run at the tip");
    if (verdict === "fail") fails++;
  }
  if (scn.flaky) {
    check(fails >= 8 && fails <= 32, scn.branch, `flaky test failed ${fails}/40 runs, expected roughly half`);
  } else {
    check(fails === 1, scn.branch, "target test should fail at the tip");
  }
  for (let n = 0; n < 60 && !log; n++) {
    const run = runTests();
    if (!run.ok) log = run.out;
  }
  check(log, scn.branch, "could not capture a failing test log");
  fs.mkdirSync(path.join(OUT, "logs"), { recursive: true });
  fs.writeFileSync(path.join(OUT, "logs", `${scn.name}.log`), log);

  return {
    name: scn.name,
    branch: scn.branch,
    summary: scn.summary,
    expected: scn.expected,
    escalate_stage: scn.escalateStage,
    flaky: Boolean(scn.flaky),
    good,
    bad: git("rev-parse", "HEAD"),
    commits: ops.length,
    target_test: scn.target.name,
    test_file: scn.target.file,
    culprit,
    culprit_index: failingFrom + 1,
    unbuildable,
    log: `logs/${scn.name}.log`,
    flaky_failures: scn.flaky ? `${fails}/40` : null,
  };
}

// ---------------------------------------------------------------------------

if (fs.existsSync(OUT)) {
  // Only delete a directory this script created.
  if (fs.readdirSync(OUT).length > 0 && !fs.existsSync(MANIFEST)) {
    fail(`${OUT} exists and was not created by this script; remove it or pass --out`);
  }
  fs.rmSync(OUT, { recursive: true, force: true });
}
fs.mkdirSync(OUT, { recursive: true });
// Written first so an interrupted run can still be cleaned up by the next one.
fs.writeFileSync(MANIFEST, "{}\n");

const started = Date.now();
const good = buildMain();
const scenarios = SCENARIOS.map((scn) => {
  console.log(`building ${scn.branch} (verify: ${VERIFY})`);
  return buildScenario(scn, good);
});
git("checkout", "-q", "main");

fs.writeFileSync(MANIFEST, `${JSON.stringify({ repo: "repo", good, scenarios }, null, 2)}\n`);

console.log(`\nrepo: ${REPO}`);
console.log(`good (main): ${good.slice(0, 10)}`);
for (const s of scenarios) {
  const culprit = `${s.culprit.slice(0, 10)} (commit ${s.culprit_index} of ${s.commits})`;
  const extra = s.flaky ? `, target failed ${s.flaky_failures} runs` : "";
  console.log(`${s.branch}: bad ${s.bad.slice(0, 10)}, culprit ${culprit}, expect ${s.expected}${extra}`);
}
console.log(`done in ${((Date.now() - started) / 1000).toFixed(1)}s`);

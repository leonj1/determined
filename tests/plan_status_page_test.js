const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const pagePath = path.join(__dirname, "..", "src", "clients", "plan_status_page.html");
const page = fs.readFileSync(pagePath, "utf8");
const scripts = [...page.matchAll(/<script(?: [^>]*)?>([\s\S]*?)<\/script>/g)];
const pageScript = scripts.at(-1)[1];
const markedSource = fs.readFileSync(
  path.join(__dirname, "..", "src", "clients", "assets", "marked.min.js"),
  "utf8",
);

class FakeClassList {
  constructor() { this.values = new Set(); }
  add(...names) { names.forEach((name) => this.values.add(name)); }
  remove(...names) { names.forEach((name) => this.values.delete(name)); }
  contains(name) { return this.values.has(name); }
  toggle(name, force) {
    const enabled = force === undefined ? !this.contains(name) : force;
    if (enabled) this.add(name); else this.remove(name);
    return enabled;
  }
}

class FakeNode {
  constructor(tagName, registry) {
    this.tagName = tagName.toUpperCase();
    this.registry = registry;
    this.classList = new FakeClassList();
    this.children = [];
    this.listeners = {};
    this.style = { setProperty: (name, value) => { this.style[name] = value; } };
    this.dataset = {};
    this.scrolls = [];
    this.attributes = {};
    this._innerHTML = "";
    this._id = "";
    this.textContent = "";
    this.offsetHeight = 0;
  }

  set id(value) { this._id = value; if (value) this.registry.set(value, this); }
  get id() { return this._id; }
  set innerHTML(value) {
    this._innerHTML = value;
    for (const match of value.matchAll(/ id="([^"]+)"/g)) new FakeNode("h2", this.registry).id = match[1];
  }
  get innerHTML() { return this._innerHTML; }
  append(...children) {
    children.forEach((child) => { if (child instanceof FakeNode) child.parentElement = this; });
    this.children.push(...children);
  }
  before(...nodes) { this.beforeNodes = (this.beforeNodes || []).concat(nodes); }
  replaceChildren(...children) {
    children.forEach((child) => { if (child instanceof FakeNode) child.parentElement = this; });
    this.children = children;
  }
  addEventListener(name, listener) { this.listeners[name] = listener; }
  setAttribute(name, value) { this.attributes[name] = value; }
  scrollIntoView(options) { this.scrolls.push(options); }
}

function createPageEnvironment(location = { pathname: "/goal", hash: "" }) {
  const registry = new Map();
  const nodes = [...page.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]);
  nodes.forEach((id) => { const node = new FakeNode("div", registry); node.id = id; });
  const tabs = createTabNodes(registry, ["goal", "plan", "tests", "steps", "log", "exec", "explain", "quiz"]);
  const panels = tabs.map((tab) => registry.get("panel-" + tab.dataset.tab));
  const testTabs = createTestTabNodes(registry);
  const windowListeners = {};
  const document = createFakeDocument(registry, tabs, panels, testTabs);
  const historyCalls = [];
  const window = createFakeWindow(location, windowListeners);
  const context = vm.createContext(createFakeGlobals(document, window, historyCalls));
  vm.runInContext(markedSource, context);
  vm.runInContext(pageScript, context);
  return { context, historyCalls, registry, run: (source) => vm.runInContext(source, context) };
}

function createTabNodes(registry, names) {
  return names.map((name) => {
    const node = new FakeNode("button", registry);
    node.dataset.tab = name;
    return node;
  });
}

function createTestTabNodes(registry) {
  return ["journey", "bdd"].map((name) => {
    const node = new FakeNode("button", registry);
    node.dataset.testsTab = name;
    return node;
  });
}

function createFakeDocument(registry, tabs, panels, testTabs) {
  const document = {
    body: { scrollHeight: 0 },
    title: "determined — planning",
    documentElement: new FakeNode("html", registry),
    createElement: (tagName) => new FakeNode(tagName, registry),
    getElementById: (id) => registry.get(id) || null,
    querySelectorAll: (selector) => selectNodes(selector, tabs, panels, testTabs),
  };
  return document;
}

function selectNodes(selector, tabs, panels, testTabs) {
  if (selector === "#tabs button") return tabs;
  if (selector === "#content section.panel") return panels;
  if (selector === "#tests-tabs button") return testTabs;
  return [];
}

function createFakeWindow(location, listeners) {
  return {
    addEventListener: (name, listener) => { listeners[name] = listener; },
    confirm: () => true,
    innerHeight: 800,
    location: { ...location },
    scrollTo: () => {},
    scrollY: 0,
  };
}

function createFakeGlobals(document, window, historyCalls) {
  return {
    Diff2Html: { html: () => "DIFF2HTML_MARKER" },
    EventSource: class { constructor() { this.onmessage = null; this.onerror = null; } },
    console,
    document,
    fetch: () => Promise.resolve(),
    history: { pushState: (_state, _title, url) => historyCalls.push(url) },
    localStorage: { getItem: () => null, removeItem: () => {}, setItem: () => {} },
    setInterval: () => 1,
    window,
  };
}

function collectDescendants(node) {
  return node.children.flatMap((child) => child instanceof FakeNode ? [child, ...collectDescendants(child)] : []);
}

test("slug output matches the anchor contract", () => {
  const browser = createPageEnvironment();
  assert.equal(browser.run(`slugify("Widget: behavior")`), "widget-behavior");
  assert.equal(browser.run(`slugify("Widget behavior!")`), "widget-behavior");
  assert.equal(browser.run(`slugify("Café behavior")`), "caf-behavior");
});

test("heading ids use the requested document anchor contract", () => {
  const browser = createPageEnvironment();
  const plain = browser.run(`markdownToHtml("## Widget behavior")`);
  const explanation = browser.run(`markdownToHtml("## Widget behavior", { headingIds: true })`);
  const plan = browser.run(`markdownToHtml("## Widget behavior", {
    headingIds: true, headingPrefix: "plan--", headingLevels: [2, 3, 4]
  })`);
  assert.equal(plain, "<h2>Widget behavior</h2>");
  assert.equal(explanation, '<h2 id="explain--widget-behavior">Widget behavior</h2>');
  assert.equal(plan, '<h2 id="plan--widget-behavior">Widget behavior</h2>');
});

test("nested headings take no body id and stay in lockstep with planSections", () => {
  const browser = createPageEnvironment();
  const input = "## A\n\n> ## A\n\n## A";
  const html = browser.run(`markdownToHtml(${JSON.stringify(input)}, {
    headingIds: true, headingPrefix: "plan--", headingLevels: [2, 3, 4]
  })`);
  assert(html.includes('<h2 id="plan--a">A</h2>'), html);
  assert(html.includes('<h2 id="plan--a-2">A</h2>'), html);
  assert(!html.includes("plan--a-3"), html);
  assert(/<blockquote>\s*<h2>A<\/h2>\s*<\/blockquote>/.test(html), html);

  const sectionIds = browser.run(
    `planSections(${JSON.stringify(input)}).map((s) => s.id).join(",")`
  );
  assert.equal(sectionIds, "plan--a,plan--a-2");

  const listInput = "- ## B\n\n## B";
  const listHtml = browser.run(`markdownToHtml(${JSON.stringify(listInput)}, {
    headingIds: true, headingPrefix: "plan--", headingLevels: [2, 3, 4]
  })`);
  assert(listHtml.includes('<h2 id="plan--b">B</h2>'), listHtml);
  assert(!listHtml.includes("plan--b-2"), listHtml);
  const listSectionIds = browser.run(
    `planSections(${JSON.stringify(listInput)}).map((s) => s.id).join(",")`
  );
  assert.equal(listSectionIds, "plan--b");
});

test("fence styles marked lexes as code stay in lockstep with planSections", () => {
  const browser = createPageEnvironment();
  const options = `{ headingIds: true, headingPrefix: "plan--", headingLevels: [2, 3, 4] }`;

  const tildeInput = "~~~\n## A\n~~~\n\n## A\n\n## A";
  const tildeHtml = browser.run(
    `markdownToHtml(${JSON.stringify(tildeInput)}, ${options})`
  );
  assert(tildeHtml.includes('<h2 id="plan--a">A</h2>'), tildeHtml);
  assert(tildeHtml.includes('<h2 id="plan--a-2">A</h2>'), tildeHtml);
  assert(!tildeHtml.includes("plan--a-3"), tildeHtml);
  const tildeSectionIds = browser.run(
    `planSections(${JSON.stringify(tildeInput)}).map((s) => s.id).join(",")`
  );
  assert.equal(tildeSectionIds, "plan--a,plan--a-2");

  const indentedInput = "  ```\n## C\n  ```\n\n## C";
  const indentedHtml = browser.run(
    `markdownToHtml(${JSON.stringify(indentedInput)}, ${options})`
  );
  assert(indentedHtml.includes('<h2 id="plan--c">C</h2>'), indentedHtml);
  assert(!indentedHtml.includes("plan--c-2"), indentedHtml);
  const indentedSectionIds = browser.run(
    `planSections(${JSON.stringify(indentedInput)}).map((s) => s.id).join(",")`
  );
  assert.equal(indentedSectionIds, "plan--c");
});

test("the Plan tab has links to each plan section", () => {
  const browser = createPageEnvironment();
  const plan = "# Delivery plan\n\n## User flow\n\nDetails.\n\n### Empty state\n\nMore.\n\n" +
    "## User flow\n\nAgain.\n\n```md\n## Example only\n```";
  browser.run("renderPlan(" + JSON.stringify(plan) + ")");

  const links = collectDescendants(browser.registry.get("plan-section-list"))
    .filter((node) => node.tagName === "A");
  assert.deepEqual(links.map((link) => link.textContent), ["User flow", "Empty state", "User flow"]);
  assert.deepEqual(links.map((link) => link.href), [
    "/plan#plan--user-flow",
    "/plan#plan--empty-state",
    "/plan#plan--user-flow-2",
  ]);
  assert.equal(browser.registry.get("plan-sections").hidden, false);
});

test("a plan section link opens and scrolls to its section", () => {
  const browser = createPageEnvironment({ pathname: "/goal", hash: "" });
  browser.run(`renderPlan("## User flow\\n\\nDetails.")`);
  const link = collectDescendants(browser.registry.get("plan-section-list"))
    .find((node) => node.tagName === "A");

  link.listeners.click({ preventDefault: () => {} });

  assert.equal(browser.historyCalls.at(-1), "/plan#plan--user-flow");
  assert(browser.registry.get("panel-plan").classList.contains("active"));
  assert.equal(browser.registry.get("plan--user-flow").scrolls.length, 1);
});

test("direct plan hashes scroll after plan markdown renders", () => {
  const browser = createPageEnvironment({ pathname: "/plan", hash: "#plan--user-flow" });
  browser.run(`renderPlan("## User flow\\n\\nDetails.")`);
  const heading = browser.registry.get("plan--user-flow");
  assert.equal(heading.scrolls.length, 1);
  assert(heading.classList.contains("link-flash"));
  browser.run(`renderPlan("## User flow\\n\\nDetails.")`);
  assert.equal(browser.registry.get("plan--user-flow").scrolls.length, 0);
});

test("a missing source still opens the explanation tab", () => {
  const browser = createPageEnvironment({ pathname: "/quiz", hash: "" });
  browser.run(`followQuizSource("Missing source")`);
  assert.equal(browser.historyCalls.at(-1), "/explain");
  assert(browser.registry.get("panel-explain").classList.contains("active"));
});

test("quiz results retain links to every source section", () => {
  const browser = createPageEnvironment();
  browser.run(`
    quizAttempt = { questions: [
      { question: "First?", correctIndex: 0, sourceSection: "First change" },
      { question: "Second?", correctIndex: 1, sourceSection: "Second change" }
    ], answers: [0, 0], results: true };
    globalThis.resultsCard = document.createElement("div");
    renderQuizResults(globalThis.resultsCard);
  `);
  const links = collectDescendants(browser.context.resultsCard).filter((node) => node.tagName === "A");
  assert.deepEqual(links.map((link) => link.href), [
    "/explain#explain--first-change",
    "/explain#explain--second-change",
  ]);
});

test("direct explanation hashes scroll after markdown renders", () => {
  const browser = createPageEnvironment({ pathname: "/explain", hash: "#explain--widget-behavior" });
  browser.run(`renderExplanation({ explainPhase: "succeeded", explanation: "Intro.\\n\\n## Widget behavior\\n\\nDetails." })`);
  const heading = browser.registry.get("explain--widget-behavior");
  assert.equal(heading.scrolls.length, 1);
  assert(heading.classList.contains("link-flash"));
  browser.run(`renderExplanation({ explainPhase: "succeeded", explanation: "Intro.\\n\\n## Widget behavior\\n\\nDetails." })`);
  assert.equal(heading.scrolls.length, 1);
});

test("status updates make the lifecycle scannable and identify browser-tab progress", () => {
  const browser = createPageEnvironment();
  browser.run(`render({
    git: { remote: "https://github.com/leonj1/determined.git", branch: "master" },
    tool: { name: "claude" }, phase: "succeeded", goal: "Goal", plan: "Plan", tests: "Tests",
    taskSteps: [{ text: "Build", completed: false }], log: [{}], steps: [], pendingAnnotations: [],
    execPhase: "running", execStartedAt: "2026-07-20T10:00:00Z", execLog: [],
    explainPhase: "", quizPhase: "", implementOffered: false,
  })`);
  assert.equal(browser.registry.get("remote").textContent, "determined");
  assert.equal(browser.registry.get("remote").title, "https://github.com/leonj1/determined.git");
  assert.equal(browser.run(`document.title`), "▶ 50% — determined");
  assert.equal(browser.run(`document.querySelectorAll("#tabs button")[0].dataset.state`), "succeeded");
  assert.equal(browser.run(`document.querySelectorAll("#tabs button")[5].dataset.state`), "running");
  assert.equal(browser.run(`document.querySelectorAll("#tabs button")[6].dataset.state`), "empty");
});

test("failed execution restores Implement without allowing successful or running retries", () => {
  const browser = createPageEnvironment();
  const visible = (execPhase) => browser.run(`
    render({
      git: {}, tool: {}, phase: "succeeded", goal: "Goal", plan: "Plan", tests: "Tests",
      taskSteps: [], log: [], steps: [], pendingAnnotations: [], execLog: [],
      execPhase: ${JSON.stringify(execPhase)}, explainPhase: "", quizPhase: "",
      implementOffered: true,
    });
    document.getElementById("implement").classList.contains("on");
  `);

  assert.equal(visible("failed"), true);
  assert.equal(visible("running"), false);
  assert.equal(visible("succeeded"), false);
  assert.equal(visible(""), true);
});

test("the Plan tab renders a generated demo in a network-isolated frame", () => {
  const browser = createPageEnvironment();
  browser.run(`renderDemo("<!doctype html><button>Try it</button>")`);

  assert.equal(browser.run(`document.getElementById("plan-demo").hidden`), false);
  const srcdoc = browser.run(`document.getElementById("demo-frame").srcdoc`);
  assert.match(srcdoc, /Content-Security-Policy/);
  assert.match(srcdoc, /default-src 'none'/);
  assert.match(srcdoc, /<button>Try it<\/button>/);

  browser.run(`renderDemo("")`);
  assert.equal(browser.run(`document.getElementById("plan-demo").hidden`), true);
  assert.equal(browser.run(`document.getElementById("demo-frame").srcdoc`), "");
});

test("only the current unfinished planning artifact is marked running", () => {
  const browser = createPageEnvironment();
  const states = browser.run(`tabStates({ phase: "running", goal: "Goal", taskSteps: [], log: [] })`);
  assert.deepEqual({ ...states }, {
    goal: "succeeded", plan: "running", tests: "empty", steps: "empty", log: "empty",
    exec: "empty", explain: "empty", quiz: "empty",
  });
});

test("the active activity entry offers confirmed Skip and Stop controls", () => {
  const browser = createPageEnvironment();
  browser.run(`
    globalThis.fetchCalls = [];
    fetch = (path) => { fetchCalls.push(path); return Promise.resolve({ ok: true }); };
    window.confirm = () => false;
    globalThis.activitySteps = [{ at: "2026-07-22T10:00:00Z", message: "executing step 1: build" }];
    renderActivity(activitySteps, activitySteps[0], true);
  `);
  const buttons = collectDescendants(browser.registry.get("activity-log"))
    .filter((node) => node.tagName === "BUTTON");
  assert.deepEqual(buttons.map((button) => button.textContent), ["Skip", "Stop"]);

  buttons[0].listeners.click();
  assert.equal(browser.run("fetchCalls.length"), 0, "a declined confirmation must not post");

  browser.run("window.confirm = () => true");
  buttons[0].listeners.click();
  assert.equal(browser.run("JSON.stringify(fetchCalls)"), '["/task/skip"]');
  assert.ok(buttons.every((button) => button.disabled), "both controls lock while the request is pending");
  buttons[1].listeners.click();
  assert.equal(browser.run("fetchCalls.length"), 1, "locked controls must not post again");

  browser.run("taskActionPending = false; renderActivity(activitySteps, activitySteps[0], false)");
  const remaining = collectDescendants(browser.registry.get("activity-log"))
    .filter((node) => node.tagName === "BUTTON");
  assert.equal(remaining.length, 0, "no controls without an active cancellable task");
});

test("a Stop confirmation posts to the stop endpoint", () => {
  const browser = createPageEnvironment();
  browser.run(`
    globalThis.fetchCalls = [];
    fetch = (path) => { fetchCalls.push(path); return Promise.resolve({ ok: true }); };
    globalThis.activitySteps = [{ at: "2026-07-22T10:00:00Z", message: "verifying step 2" }];
    renderActivity(activitySteps, activitySteps[0], true);
  `);
  const stop = collectDescendants(browser.registry.get("activity-log"))
    .find((node) => node.tagName === "BUTTON" && node.textContent === "Stop");
  stop.listeners.click();
  assert.equal(browser.run("JSON.stringify(fetchCalls)"), '["/task/stop"]');
});

test("sticky offsets follow the rendered header and progress heights", () => {
  const browser = createPageEnvironment();
  browser.registry.get("page-header").offsetHeight = 72;
  browser.registry.get("progress").offsetHeight = 31;
  browser.registry.get("tabs").offsetHeight = 44;
  browser.run(`syncStickyOffsets()`);
  assert.equal(browser.run(`document.documentElement.style["--header-height"]`), "72px");
  assert.equal(browser.run(`document.documentElement.style["--progress-height"]`), "31px");
  assert.equal(browser.run(`document.documentElement.style["--tabs-height"]`), "44px");
});


test("raw HTML is escaped and never passed through", () => {
  const browser = createPageEnvironment();
  const html = browser.run(`markdownToHtml("hello <b>x</b>")`);
  assert(html.includes("hello &lt;b&gt;x&lt;/b&gt;"));
  assert(!html.includes("<b>"));
});

test("links open in a blank noopener target", () => {
  const browser = createPageEnvironment();
  const html = browser.run(`markdownToHtml("[x](https://example.com)")`);
  assert.equal(html, '<p><a href="https://example.com" target="_blank" rel="noopener">x</a></p>\n');
});

test("a javascript: link scheme is not rendered as an executable href", () => {
  const browser = createPageEnvironment();
  const lower = browser.run(`markdownToHtml("[x](javascript:alert(1))")`);
  assert(!lower.includes("href="), lower);
  assert(!/href="[^"]*javascript:/.test(lower));
  assert(lower.includes("x"));
  const mixed = browser.run(`markdownToHtml("[x](JaVaScRiPt:alert(1))")`);
  assert(!/href="[^"]*javascript:/i.test(mixed), mixed);
});

test("a javascript: image scheme is not rendered as an executable src", () => {
  const browser = createPageEnvironment();
  const html = browser.run(`markdownToHtml("![x](javascript:alert(1))")`);
  assert(!/src="[^"]*javascript:/i.test(html), html);
  assert(!html.includes("<img"), html);
});

test("http/https/mailto and relative link schemes still render as anchors", () => {
  const browser = createPageEnvironment();
  assert(browser.run(`markdownToHtml("[x](https://example.com)")`).includes('href="https://example.com"'));
  assert(browser.run(`markdownToHtml("[x](mailto:a@b.com)")`).includes('href="mailto:a@b.com"'));
  assert(browser.run(`markdownToHtml("[x](/plan#foo)")`).includes('href="/plan#foo"'));
});

test("fenced code dispatches to the syntax highlighter", () => {
  const browser = createPageEnvironment();
  const html = browser.run("markdownToHtml(\"```go\\nreturn nil\\n```\")");
  assert(html.startsWith("<pre><code>"));
  assert(html.includes('class="tok-keyword"'));
});

test("a flattened one-line table renders as a GFM table", () => {
  const browser = createPageEnvironment();
  const html = browser.run(`markdownToHtml("| a | b | |---|---| | 1 | 2 |")`);
  assert(html.includes("<table>"));
  assert(html.includes("<td>1</td>"));
  assert(html.includes("<td>2</td>"));
});

test("a diff fence dispatches to Diff2Html verbatim", () => {
  const browser = createPageEnvironment();
  const html = browser.run("markdownToHtml(\"```diff\\n-a\\n+b\\n```\")");
  assert(html.includes("DIFF2HTML_MARKER"));
});

test("a mermaid sequenceDiagram fence renders a hand-built SVG", () => {
  const browser = createPageEnvironment();
  const html = browser.run("markdownToHtml(\"```mermaid\\nsequenceDiagram\\n  A->>B: hi\\n```\")");
  assert(html.includes("<svg"));
  assert(html.includes("seq-diagram"));
});

test("a raw <script> snippet renders escaped and never executes", () => {
  const browser = createPageEnvironment();
  const html = browser.run("markdownToHtml(\"<script>alert(1)</script>\")");
  assert(html.includes("&lt;script&gt;"));
  assert(!html.includes("<script>"));
});

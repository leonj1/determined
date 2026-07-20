const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const pagePath = path.join(__dirname, "..", "src", "clients", "plan_status_page.html");
const page = fs.readFileSync(pagePath, "utf8");
const scripts = [...page.matchAll(/<script(?: [^>]*)?>([\s\S]*?)<\/script>/g)];
const pageScript = scripts.at(-1)[1];

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
    this.style = {};
    this.dataset = {};
    this.scrolls = [];
    this.attributes = {};
    this._innerHTML = "";
    this._id = "";
    this.textContent = "";
  }

  set id(value) { this._id = value; if (value) this.registry.set(value, this); }
  get id() { return this._id; }
  set innerHTML(value) {
    this._innerHTML = value;
    for (const match of value.matchAll(/ id="([^"]+)"/g)) new FakeNode("h2", this.registry).id = match[1];
  }
  get innerHTML() { return this._innerHTML; }
  append(...children) { this.children.push(...children); }
  before(...nodes) { this.beforeNodes = (this.beforeNodes || []).concat(nodes); }
  replaceChildren(...children) { this.children = children; }
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
    innerHeight: 800,
    location: { ...location },
    scrollTo: () => {},
    scrollY: 0,
  };
}

function createFakeGlobals(document, window, historyCalls) {
  return {
    Diff2Html: { html: () => "" },
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

test("only explanation markdown receives heading ids", () => {
  const browser = createPageEnvironment();
  const plain = browser.run(`markdownToHtml("## Widget behavior")`);
  const explanation = browser.run(`markdownToHtml("## Widget behavior", { headingIds: true })`);
  assert.equal(plain, "<h2>Widget behavior</h2>");
  assert.equal(explanation, '<h2 id="explain--widget-behavior">Widget behavior</h2>');
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

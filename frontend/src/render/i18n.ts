// The words on screen, in the user's language.
//
// Messages live in src/locales/<code>.json, one flat object per locale, shared
// with the Go side (internal/i18n), which reads the same files. English is
// bundled here and is the fallback for every missing key, so a screen never
// shows a blank. The other locales come from Go (GetLanguageState), which drops
// any translation whose English has changed since it was made.
//
// Messages use a small part of ICU MessageFormat: {name}, {n, plural, ...} with
// # for the number, and {x, select, ...}. internal/i18n/format.go implements the
// same rules, and both run internal/i18n/testdata/vectors.json.
//
// Pure apart from the current locale, which starts as English: the Node harness
// imports this file and gets the English the checks assert on.

import en from "../locales/en.json" with { type: "json" };

/** The locales we ship, in the order the picker lists them. internal/i18n.Locales is the same list. */
export const LOCALES = [
  "en", "es", "pt-BR", "fr", "de", "it", "nl", "pl", "ru", "uk",
  "tr", "ar", "hi", "id", "vi", "th", "fil", "zh-Hans", "ja", "ko",
] as const;

export type Locale = (typeof LOCALES)[number];

const english: Record<string, string> = en as Record<string, string>;
const RTL = new Set(["ar"]);

let current: Locale = "en";
let messages: Record<string, string> = english;

/** Old or sibling language codes, mapped to the one we ship. */
const ALIASES: Record<string, string> = { tl: "fil", in: "id" };

function matchOne(raw: string): Locale | "" {
  let tag = (raw || "").trim();
  const cut = tag.search(/[.@]/);
  if (cut >= 0) tag = tag.slice(0, cut);
  tag = tag.replace(/_/g, "-");
  if (!tag || /^(c|posix)$/i.test(tag)) return "";
  const exact = LOCALES.find((code) => code.toLowerCase() === tag.toLowerCase());
  if (exact) return exact;
  let base = tag.split("-")[0].toLowerCase();
  base = ALIASES[base] ?? base;
  return LOCALES.find((code) => code.split("-")[0].toLowerCase() === base) ?? "";
}

/**
 * The first supported locale for a list of BCP 47 tags, in the user's order, or
 * "" when none matches. es-MX gets es, pt-PT gets pt-BR, zh-TW gets zh-Hans.
 */
export function matchLocale(tags: readonly string[]): Locale | "" {
  for (const tag of tags) {
    const code = matchOne(tag);
    if (code) return code;
  }
  return "";
}

/** Makes code the current locale. `translated` are its messages; missing ones show English. */
export function setLocale(code: string, translated: Record<string, string> = {}) {
  const match = matchOne(code);
  current = match || "en";
  messages = current === "en" ? english : { ...english, ...nonEmpty(translated) };
}

function nonEmpty(m: Record<string, string>) {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(m)) if (typeof v === "string" && v !== "") out[k] = v;
  return out;
}

export function currentLocale(): Locale {
  return current;
}

/** "rtl" for Arabic, "ltr" for the rest. */
export function textDirection(code: string = current): "rtl" | "ltr" {
  return RTL.has(code) ? "rtl" : "ltr";
}

/** The BCP 47 tag Intl formats numbers, money and dates with. */
export function intlLocale(): string {
  return current;
}

export type Args = Record<string, string | number | RawHtml | undefined | null>;

/** The message for key in the current locale, as plain text. Escape it before an innerHTML sink, or use ht. */
export function t(key: string, args: Args = {}): string {
  return format(current, lookup(key), args, (s) => s, (v) => (v instanceof RawHtml ? v.html : String(v)));
}

/**
 * The message for key as HTML. The message text and every argument are escaped;
 * a raw(...) argument goes in as it is. The message may use <strong>, <em>,
 * <code>, <b> and <kbd> with no attributes, so a sentence can keep its bold
 * words in every language.
 */
export function ht(key: string, args: Args = {}): string {
  return format(current, lookup(key), args, escapeText, (v) => (v instanceof RawHtml ? v.html : escapeHtml(String(v))));
}

/** Marks HTML that ht must not escape: a link or a button built by the caller. */
export class RawHtml {
  constructor(readonly html: string) {}
}
export function raw(html: string) {
  return new RawHtml(html);
}

function lookup(key: string): string {
  const msg = messages[key] ?? english[key];
  // A key in no file is a bug the parity check catches. Showing the key keeps
  // the screen readable enough to report it.
  return msg === undefined ? key : msg;
}

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#039;" })[ch] || ch);
}

// A message's own words: & < > and " are escaped; the apostrophe is kept, so
// "router's" draws the same bytes as the English template did. Every attribute
// in this app is double-quoted, so an apostrophe cannot end one.
function escapeText(text: string): string {
  return text
    .replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[ch] || ch)
    .replace(/&lt;(\/?)(strong|em|code|b|kbd)&gt;/g, "<$1$2>");
}

/** The CLDR cardinal category of n in locale: zero, one, two, few, many or other. */
export function pluralCategory(locale: string, n: number): string {
  try {
    return new Intl.PluralRules(locale).select(n);
  } catch {
    return "other";
  }
}

type Node =
  | { kind: "text"; text: string }
  | { kind: "pound" }
  | { kind: "arg"; name: string }
  | { kind: "plural" | "select"; name: string; cases: Map<string, Node[]> };

class Parser {
  pos = 0;
  constructor(readonly src: string) {}

  parse(inCase: boolean): Node[] | null {
    const nodes: Node[] = [];
    let text = "";
    const flush = () => {
      if (text) nodes.push({ kind: "text", text });
      text = "";
    };
    const s = this.src;
    while (this.pos < s.length) {
      const c = s[this.pos];
      if (c === "'") {
        if (s[this.pos + 1] === "'") {
          text += "'";
          this.pos += 2;
          continue;
        }
        if (this.pos + 1 < s.length && "{}#".includes(s[this.pos + 1])) {
          // A quoted span runs to the next lone apostrophe; two apostrophes
          // inside it are one, as everywhere else.
          this.pos++;
          while (this.pos < s.length) {
            if (s[this.pos] === "'") {
              if (s[this.pos + 1] === "'") {
                text += "'";
                this.pos += 2;
                continue;
              }
              this.pos++;
              break;
            }
            text += s[this.pos++];
          }
          continue;
        }
        text += c;
        this.pos++;
      } else if (c === "#" && inCase) {
        flush();
        nodes.push({ kind: "pound" });
        this.pos++;
      } else if (c === "{") {
        flush();
        const n = this.argument();
        if (!n) return null;
        nodes.push(n);
      } else if (c === "}") {
        if (!inCase) return null;
        flush();
        return nodes;
      } else {
        text += c;
        this.pos++;
      }
    }
    if (inCase) return null;
    flush();
    return nodes;
  }

  argument(): Node | null {
    const s = this.src;
    this.pos++;
    let end = indexOfAny(s, ",}", this.pos);
    if (end < 0) return null;
    const name = s.slice(this.pos, end).trim();
    this.pos = end;
    if (!name) return null;
    if (s[this.pos] === "}") {
      this.pos++;
      return { kind: "arg", name };
    }
    this.pos++;
    end = indexOfAny(s, ",}", this.pos);
    if (end < 0) return null;
    const kind = s.slice(this.pos, end).trim();
    this.pos = end;
    if ((kind !== "plural" && kind !== "select") || s[this.pos] !== ",") return null;
    this.pos++;
    const cases = new Map<string, Node[]>();
    for (;;) {
      this.skipSpace();
      if (this.pos >= s.length) return null;
      if (s[this.pos] === "}") {
        this.pos++;
        break;
      }
      const start = this.pos;
      while (this.pos < s.length && !"{ \n\t".includes(s[this.pos])) this.pos++;
      const sel = s.slice(start, this.pos);
      this.skipSpace();
      if (!sel || s[this.pos] !== "{") return null;
      this.pos++;
      const body = this.parse(true);
      if (!body) return null;
      this.pos++;
      cases.set(sel, body);
    }
    if (!cases.has("other")) return null;
    return { kind: kind as "plural" | "select", name, cases };
  }

  skipSpace() {
    while (this.pos < this.src.length && " \n\t\r".includes(this.src[this.pos])) this.pos++;
  }
}

function indexOfAny(s: string, chars: string, from: number) {
  for (let i = from; i < s.length; i++) if (chars.includes(s[i])) return i;
  return -1;
}

/**
 * Fills one message. A message that does not parse is returned as written, so a
 * broken translation still shows its words. text transforms the message's own
 * words; value turns an argument into what goes on screen.
 */
export function format(
  locale: string,
  msg: string,
  args: Args = {},
  text: (s: string) => string = (s) => s,
  value: (v: unknown) => string = (v) => (v instanceof RawHtml ? v.html : String(v)),
): string {
  if (!/[{}']/.test(msg)) return text(msg);
  const p = new Parser(msg);
  const nodes = p.parse(false);
  if (!nodes || p.pos !== msg.length) return text(msg);
  const numbers = new Intl.NumberFormat(locale);
  const out: string[] = [];
  const render = (list: Node[], n: number | null) => {
    for (const node of list) {
      if (node.kind === "text") out.push(text(node.text));
      else if (node.kind === "pound") out.push(n === null ? "#" : numbers.format(n));
      else if (node.kind === "arg") {
        const v = args[node.name];
        out.push(v === undefined || v === null ? text(`{${node.name}}`) : value(v));
      } else if (node.kind === "select") {
        const v = String(args[node.name] ?? "");
        render(node.cases.get(v) ?? node.cases.get("other")!, n);
      } else {
        const v = Number(args[node.name]);
        if (args[node.name] === undefined || args[node.name] === null || !Number.isFinite(v)) {
          render(node.cases.get("other")!, null);
          continue;
        }
        // A fraction is not a count: it takes the other case and shows as
        // written, here and in Go alike.
        if (!Number.isInteger(v)) {
          render(node.cases.get("other")!, v);
          continue;
        }
        const body = node.cases.get(`=${v}`) ?? node.cases.get(pluralCategory(locale, v)) ?? node.cases.get("other")!;
        render(body, v);
      }
    }
  };
  render(nodes, null);
  return out.join("");
}

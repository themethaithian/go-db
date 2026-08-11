// Whether a Redis string reply (ReplyValue.svelte, ValueView.svelte) is
// worth offering as a JsonTree instead of raw text, and what it parses to.
// A digit string ("123") or a quoted word ("true") parses fine under
// JSON.parse too, so parsing alone does not qualify a reply for the tree —
// only a value whose trimmed text actually *opens* an object or array does.
// That is RedisInsight's own rule, and it is what keeps a plain numeric
// string from ever being offered a JSON view it has no shape for.
export function parseJsonReply(text: string): unknown | undefined {
  const trimmed = text.trim();
  const first = trimmed[0];
  if (first !== "{" && first !== "[") return undefined;
  try {
    return JSON.parse(trimmed);
  } catch {
    return undefined;
  }
}

// RedisInsight's own threshold: a short JSON-shaped string reads fine as
// text, so Raw stays the default below this length; at or above it, the
// tree is what saves the eye the parsing work, so JSON is the default (the
// toggle still lets either view win, whichever a value defaults to).
export const JSON_REPLY_THRESHOLD = 80;

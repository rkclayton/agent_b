// Item 2ji (b): where the run's time went, as a sentence.
//
// The operator was shown a dense one-line version of this and said: "that's not
// very human readable." So this is plain words, one line, largest share first,
// and nothing under five per cent is mentioned at all — a run is not more
// legible for listing every bucket that rounded to nothing.
//
// The figures come from run.stopped, which item 2ji (a) put them on. A bucket the
// server could not supply is ABSENT there, and absent is rendered as "not
// reported" in the hover and left out of the sentence. It is never a zero.

const minimumShare = 0.05;
// Under ten seconds there is nothing to apportion that a person would act on, so
// the line is just the duration.
const detailFloorMS = 10_000;

function clock(milliseconds) {
  const value = Math.max(0, Math.round(Number(milliseconds) || 0));
  if (value < 1000) return `${value} ms`;
  const seconds = Math.round(value / 1000);
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (minutes < 60) return rest ? `${minutes} min ${rest} s` : `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  return restMinutes ? `${hours} h ${restMinutes} min` : `${hours} h`;
}

function count(value, singular, plural) {
  const number = Number(value) || 0;
  if (number <= 0) return "";
  const word = number === 1 ? singular : plural;
  const spelled = { 1: "one", 2: "two", 3: "three" }[number] || String(number);
  return `${spelled} ${word}`;
}

// The model's own split, when the server reported it: a run that spent its model
// time reading the prompt and a run that spent it writing are different runs, and
// this is the only place that difference is visible.
function modelAside(time) {
  const prompt = Number(time.prompt_ms);
  const generation = Number(time.generation_ms);
  if (!Number.isFinite(prompt) && !Number.isFinite(generation)) return "";
  const total = (Number.isFinite(prompt) ? prompt : 0) + (Number.isFinite(generation) ? generation : 0);
  if (total <= 0) return "";
  if (Number.isFinite(generation) && generation / total >= 0.6) return " (mostly writing)";
  if (Number.isFinite(prompt) && prompt / total >= 0.6) return " (mostly reading the conversation)";
  return "";
}

export function runTimeBuckets(time) {
  if (!time || typeof time !== "object") return [];
  return [
    { key: "model_ms", ms: Number(time.model_ms) || 0, words: (t) => `the model ${clock(t.model_ms)}${modelAside(t)}` },
    { key: "tool_ms", ms: Number(time.tool_ms) || 0, words: (t) => `tools ${clock(t.tool_ms)}` },
    { key: "waiting_ms", ms: Number(time.waiting_ms) || 0, words: (t) => `waiting on you ${clock(t.waiting_ms)}` },
    { key: "compaction_ms", ms: Number(time.compaction_ms) || 0, words: (t) => `summarising the conversation ${clock(t.compaction_ms)}` },
    { key: "unaccounted_ms", ms: Number(time.unaccounted_ms) || 0, words: (t) => `elsewhere ${clock(t.unaccounted_ms)}` },
  ].filter((bucket) => bucket.ms > 0);
}

// runTimeSentence renders the line. It returns an empty text when run.stopped
// carries no time at all, which is every run journalled before this item existed
// — an old run says nothing rather than saying zero.
export function runTimeSentence(data) {
  const time = data && typeof data === "object" ? data.time : null;
  if (!time || typeof time !== "object") return { text: "", title: "" };
  const total = Number(time.total_ms);
  if (!Number.isFinite(total) || total <= 0) return { text: "", title: "" };

  const tail = [
    count(data.retries, "retry", "retries"),
    count(data.compactions, "summary", "summaries"),
    count(data.empty_replies, "empty reply", "empty replies"),
    count(data.repeated_calls, "repeated call", "repeated calls"),
  ].filter(Boolean);

  if (total < detailFloorMS) {
    const brief = `Took ${clock(total)}.`;
    return { text: tail.length ? `${brief} ${sentenceCase(tail.join(", "))}.` : brief, title: runTimeHover(data) };
  }

  const shares = runTimeBuckets(time)
    .filter((bucket) => bucket.ms / total >= minimumShare)
    .sort((a, b) => b.ms - a.ms)
    .map((bucket) => bucket.words(time));

  let text = `Took ${clock(total)}`;
  if (shares.length) text += ` — ${shares.join(", ")}`;
  text += tail.length ? `; ${tail.join(", ")}.` : ".";
  return { text, title: runTimeHover(data) };
}

function sentenceCase(text) {
  return text ? text[0].toUpperCase() + text.slice(1) : text;
}

// The exact figures, for the hover. This is where a bucket the server could not
// supply says so in words, rather than being quietly dropped.
export function runTimeHover(data) {
  const time = data && typeof data === "object" ? data.time : null;
  if (!time || typeof time !== "object") return "";
  const lines = [`total ${clock(time.total_ms)}`];
  for (const [label, key] of [
    ["model", "model_ms"],
    ["prompt processing", "prompt_ms"],
    ["generating", "generation_ms"],
    ["tools (sum, they overlap)", "tool_ms"],
    ["waiting", "waiting_ms"],
    ["summarising", "compaction_ms"],
    ["elsewhere", "unaccounted_ms"],
  ]) {
    const value = time[key];
    if (value === undefined || value === null) {
      lines.push(`${label}: not reported`);
      continue;
    }
    lines.push(`${label}: ${clock(value)}`);
  }
  for (const [label, key] of [
    ["retries", "retries"],
    ["summaries", "compactions"],
    ["empty replies", "empty_replies"],
    ["repeated calls", "repeated_calls"],
  ]) {
    lines.push(`${label}: ${Number(data[key]) || 0}`);
  }
  return lines.join("\n");
}

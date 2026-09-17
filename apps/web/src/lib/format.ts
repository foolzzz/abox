import { isObjectRecord } from "./data";

const DATE_FORMAT = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit"
});

const TIME_FORMAT = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit"
});

export function formatDate(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : DATE_FORMAT.format(date);
}

export function formatTime(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : TIME_FORMAT.format(date);
}

export function relativeTime(value?: string | null): string {
  if (!value) return "Unknown";
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return "Unknown";
  const deltaSeconds = Math.round((timestamp - Date.now()) / 1_000);
  const absolute = Math.abs(deltaSeconds);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (absolute < 60) return formatter.format(deltaSeconds, "second");
  if (absolute < 3_600) return formatter.format(Math.round(deltaSeconds / 60), "minute");
  if (absolute < 86_400) return formatter.format(Math.round(deltaSeconds / 3_600), "hour");
  return formatter.format(Math.round(deltaSeconds / 86_400), "day");
}

export function humanize(value: string): string {
  return value
    .replace(/[._-]+/g, " ")
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}
export function initials(value: string): string {
  return value.trim().split(/\s+/).slice(0, 2).map((part) => part[0]?.toUpperCase()).join("") || "?";
}


export function messageText(content: unknown, plainText?: string): string {
  if (plainText) return plainText;
  if (typeof content === "string") {
    try {
      const parsed: unknown = JSON.parse(content);
      if (typeof parsed === "string") return parsed;
      return extractText(parsed) || content;
    } catch {
      return content;
    }
  }
  return extractText(content) || JSON.stringify(content, null, 2);
}

export function extractText(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(extractText).filter(Boolean).join("\n");
  if (!isObjectRecord(value)) return "";
  for (const key of ["text", "plainText", "message", "content", "delta", "thinking"]) {
    if (key in value) {
      const text = extractText(value[key]);
      if (text) return text;
    }
  }
  return "";
}

export function compactJson(value: unknown): string {
  if (value === undefined) return "";
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

import { isObjectRecord } from "../lib/data";
import type { AgentEvent } from "./types";

const API_BASE = "/api/v1";
const BASE_RETRY_MS = 1_000;
const MAX_RETRY_MS = 15_000;

export type StreamState = "connecting" | "open" | "retrying" | "closed";

export interface BoxEventStreamOptions {
  signal: AbortSignal;
  lastEventId?: number;
  onEvent: (event: AgentEvent) => void;
  onState?: (state: StreamState, attempt: number) => void;
  onError?: (error: Error) => void;
  onSnapshotRequired?: () => Promise<number>;
}

interface ParsedFrame {
  id?: string;
  event?: string;
  retry?: number;
  data: string;
}

export async function streamBoxEvents(boxId: string, options: BoxEventStreamOptions): Promise<void> {
  let cursor = Math.max(0, options.lastEventId ?? 0);
  let retryMs = BASE_RETRY_MS;
  let attempt = 0;
  let seenIds = new Set<string>();

  while (!options.signal.aborted) {
    options.onState?.(attempt === 0 ? "connecting" : "retrying", attempt);
    try {
      const response = await fetch(`${API_BASE}/boxes/${encodeURIComponent(boxId)}/events`, {
        method: "GET",
        credentials: "same-origin",
        cache: "no-store",
        headers: {
          Accept: "text/event-stream",
          "Last-Event-ID": String(cursor)
        },
        signal: options.signal
      });
      if (response.status === 409 && response.headers.has("X-Agentbox-Snapshot-Required")) {
        if (!options.onSnapshotRequired) throw new Error("Event history requires a fresh snapshot");
        cursor = Math.max(0, await options.onSnapshotRequired());
        seenIds.clear();
        attempt = 0;
        continue;
      }
      if (!response.ok) throw new Error(`Event stream returned ${response.status} ${response.statusText}`);
      if (!response.body) throw new Error("Event stream response had no body");

      options.onState?.("open", attempt);
      attempt = 0;
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

      while (!options.signal.aborted) {
        const { value, done } = await reader.read();
        buffer += decoder.decode(value, { stream: !done });
        buffer = buffer.replace(/\r\n/g, "\n");

        let boundary = buffer.indexOf("\n\n");
        while (boundary >= 0) {
          const raw = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          const frame = parseFrame(raw);
          if (frame.retry !== undefined) retryMs = clamp(frame.retry, 250, MAX_RETRY_MS);
          if (frame.data) {
            const event = parseEvent(frame, boxId);
            const eventId = event.eventId || frame.id || `${event.boxId}:${event.seq}`;
            const duplicate = event.seq <= cursor || seenIds.has(eventId);
            if (!duplicate) {
              cursor = Math.max(cursor, event.seq);
              seenIds.add(eventId);
              if (seenIds.size > 2_048) seenIds = new Set(Array.from(seenIds).slice(-1_024));
              options.onEvent({ ...event, eventId });
            }
          }
          boundary = buffer.indexOf("\n\n");
        }
        if (done) break;
      }

      if (options.signal.aborted) break;
      throw new Error("Event stream disconnected");
    } catch (error) {
      if (options.signal.aborted) break;
      const normalized = error instanceof Error ? error : new Error("Event stream failed");
      options.onError?.(normalized);
      attempt += 1;
      options.onState?.("retrying", attempt);
      const wait = Promise.withResolvers<void>();
      const timer = window.setTimeout(wait.resolve, withJitter(Math.min(retryMs * 2 ** Math.min(attempt - 1, 4), MAX_RETRY_MS)));
      options.signal.addEventListener("abort", () => {
        window.clearTimeout(timer);
        wait.resolve();
      }, { once: true });
      await wait.promise;
    }
  }

  options.onState?.("closed", attempt);
}

function parseFrame(raw: string): ParsedFrame {
  const data: string[] = [];
  let id: string | undefined;
  let event: string | undefined;
  let retry: number | undefined;

  for (const line of raw.split("\n")) {
    if (!line || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    const value = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
    if (field === "data") data.push(value);
    else if (field === "id" && !value.includes("\0")) id = value;
    else if (field === "event") event = value;
    else if (field === "retry" && /^\d+$/.test(value)) retry = Number(value);
  }

  return { id, event, retry, data: data.join("\n") };
}

function parseEvent(frame: ParsedFrame, boxId: string): AgentEvent {
  const raw: unknown = JSON.parse(frame.data);
  if (!isObjectRecord(raw)) throw new Error("Event stream sent a non-object event");
  const fullEnvelope = "seq" in raw && "payload" in raw;
  const payload = fullEnvelope && isObjectRecord(raw.payload) ? raw.payload : raw;
  const rawSeq = raw.seq;
  const seq =
    typeof rawSeq === "number" && Number.isFinite(rawSeq)
      ? rawSeq
      : typeof rawSeq === "string" && /^\d+$/.test(rawSeq)
        ? Number(rawSeq)
        : frame.id && /^\d+$/.test(frame.id)
          ? Number(frame.id)
          : undefined;
  if (seq === undefined) throw new Error("Event stream event did not include a sequence");
  return {
    ...(fullEnvelope ? raw as Omit<AgentEvent, "seq" | "payload"> : {}),
    eventId: fullEnvelope && typeof raw.eventId === "string" ? raw.eventId : `${boxId}:${seq}`,
    boxId: fullEnvelope && typeof raw.boxId === "string" ? raw.boxId : boxId,
    seq,
    type: fullEnvelope && typeof raw.type === "string" ? raw.type : frame.event ?? "notice",
    occurredAt: fullEnvelope && typeof raw.occurredAt === "string" ? raw.occurredAt : new Date().toISOString(),
    payload
  };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function withJitter(value: number): number {
  return Math.round(value * (0.85 + Math.random() * 0.3));
}


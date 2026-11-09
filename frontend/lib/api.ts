export const HTTP = process.env.NEXT_PUBLIC_ENGINE_HTTP ?? "http://localhost:8080";
export const WS = process.env.NEXT_PUBLIC_ENGINE_WS ?? "ws://localhost:8080/ws";
export const ACCOUNT = "web";

export async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`${HTTP}${path}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`${path} returned ${res.status}`);
  return (await res.json()) as T;
}

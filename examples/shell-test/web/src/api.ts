export interface VM {
  hostname: string;
  hostgroup?: string;
  ip: string;
  status?: string;
}

export interface HostGroup {
  name: string;
  count: number;
  ram_bytes: number;
  cpus: number;
}

export async function listVMs(): Promise<VM[]> {
  const res = await fetch("/api/vms");
  if (!res.ok) throw new Error(await res.text());
  return (await res.json()) ?? [];
}

export async function listHostGroups(): Promise<HostGroup[]> {
  const res = await fetch("/api/hostgroups");
  if (!res.ok) throw new Error(await res.text());
  return (await res.json()) ?? [];
}

export async function launchVM(group: string): Promise<VM> {
  const res = await fetch(
    `/api/launch?group=${encodeURIComponent(group)}`,
    { method: "POST" },
  );
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export function shellWsURL(hostname: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}/ws/shell?vm=${encodeURIComponent(hostname)}`;
}

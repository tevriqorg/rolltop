import { useEffect, useState } from "react";
import { api } from "../../api";
import type { Toast } from "../../appTypes";
import { messageFromError } from "../../lib/errors";

type Status = Awaited<ReturnType<typeof api.googleConnectorStatus>>;
type Census = NonNullable<Status["connections"][number]["census"]>;

function gib(value: number) { return `${(value / 1073741824).toFixed(2)} GiB`; }

export function GoogleConnectorCard({ csrf, addToast }: {
  csrf: string;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [status, setStatus] = useState<Status | null>(null);
  const [loading, setLoading] = useState(true);
  const [starting, setStarting] = useState(false);
  const [censusRunning, setCensusRunning] = useState(false);
  const [approving, setApproving] = useState(false);
  const [syncing, setSyncing] = useState(false);

  const refresh = async () => setStatus(await api.googleConnectorStatus());
  useEffect(() => {
    let live = true;
    api.googleConnectorStatus()
      .then((data) => { if (live) setStatus(data); })
      .catch((error) => { if (live) addToast(messageFromError(error), "error"); })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, [addToast]);

  const connect = async () => {
    setStarting(true);
    try { const data = await api.startGoogleConnector(csrf); window.location.assign(data.authorization_url); }
    catch (error) { addToast(messageFromError(error), "error"); setStarting(false); }
  };
  const runCensus = async () => {
    setCensusRunning(true);
    try { await api.runGoogleCensus(csrf); await refresh(); addToast("Storage Census complete. Backfill is still locked.", "success"); }
    catch (error) { addToast(messageFromError(error), "error"); }
    finally { setCensusRunning(false); }
  };
  const approve = async () => {
    if (!window.confirm("I reviewed the Storage Census. Unlock initial Gmail backfill for this account?")) return;
    setApproving(true);
    try { await api.approveGoogleBackfill(csrf); await refresh(); addToast("Gmail backfill unlocked. No sync starts until you run it or the approved background worker checks again.", "success"); }
    catch (error) { addToast(messageFromError(error), "error"); }
    finally { setApproving(false); }
  };
  const sync = async () => {
    setSyncing(true);
    try { await api.syncGoogle(csrf); addToast("Gmail sync completed.", "success"); }
    catch (error) { addToast(messageFromError(error), "error"); }
    finally { setSyncing(false); }
  };

  if (loading) return <div className="settings-callout">Loading Google Connector…</div>;
  if (!status?.configured) return <div className="settings-callout"><strong>Google Connector</strong><p>Server OAuth credentials are not configured yet.</p></div>;
  const connected = status.connections[0];
  const census: Census | undefined = connected?.census;
  return (
    <div className="settings-callout">
      <div className="settings-index-heading">
        <div><h3>Google / Gmail</h3><p>{connected ? `${connected.email} connected.` : "Connect one Gmail account for Gate G1."}</p></div>
        {!connected ? <button className="secondary" type="button" disabled={starting} onClick={connect}>{starting ? "Opening Google…" : "Connect Google"}</button> : null}
      </div>
      {connected && !census ? <><p><strong>Storage Census required.</strong> Initial backfill is locked. Census reads message IDs and size estimates only; it does not download message bodies or attachments.</p><button className="secondary" type="button" disabled={censusRunning} onClick={runCensus}>{censusRunning ? "Scanning metadata…" : "Run Storage Census"}</button></> : null}
      {connected && census ? <div>
        <p><strong>Storage Census complete.</strong> {census.message_count.toLocaleString()} messages · estimated raw {gib(census.estimated_raw_bytes)} · Evidence estimate {gib(census.expected_evidence_bytes)} · recommended free disk {gib(census.recommended_free_disk)}.</p>
        <p>{connected.backfill_approved ? "Backfill is explicitly approved." : "Backfill remains locked until you review these figures and explicitly approve it."}</p>
        {!connected.backfill_approved ? <button type="button" disabled={approving} onClick={approve}>{approving ? "Unlocking…" : "I reviewed this Census — unlock backfill"}</button> : <button className="secondary" type="button" disabled={syncing} onClick={sync}>{syncing ? "Syncing…" : "Sync Gmail now"}</button>}
        <button className="secondary" type="button" disabled={censusRunning || approving || syncing} onClick={runCensus}>{censusRunning ? "Scanning metadata…" : "Run Census again (re-locks backfill)"}</button>
      </div> : null}
    </div>
  );
}

// The two questions asked before a service is removed, extracted from main.ts so they
// can be TESTED.
//
// Removing a service used to ask one question — "this deletes the managed container and
// its Docker volumes" — and then delete every named volume the container had. For
// Mysterium that volume is the node's keystore; for Storj it is an identity whose ID is
// proof-of-work bound, takes hours to regenerate, and whose loss forfeits the held
// payout balance. Neither is in a backup anywhere. So the user was one confirm away
// from destroying something no one could get back, and the confirm did not say so.
//
// Now removing the service removes the container and keeps the data. Deleting the data
// is a second question, and it names what is lost, volume by volume, in the words the
// catalog uses.
//
// Pure: takes the plan the backend read off the live container, returns text.

import type { DataMount, RemovalPlan } from "../wails";

// mounts is the defensive read of a Go slice, which marshals to null when empty.
function mounts(list: DataMount[] | null | undefined): DataMount[] {
  return list ?? [];
}

/** Named volumes this service keeps, which deleting the data would destroy. */
export function deletableVolumes(plan: RemovalPlan): DataMount[] {
  return mounts(plan.volumes);
}

/** Whether deleting the data would destroy something that cannot be recovered. */
export function planHasCritical(plan: RemovalPlan): boolean {
  return deletableVolumes(plan).some((volume) => volume.critical);
}

/**
 * The first question: remove the service at all.
 *
 * It states what survives, because that is the fact that changed and the one a user
 * deciding between "remove" and "stop" needs. Host folders are listed by name: a user
 * who pointed Storj at a 12 TB disk should be able to see that CashPilot is not about
 * to go near it.
 */
export function removeConfirmText(serviceName: string, plan: RemovalPlan): string {
  const lines = [`Remove ${serviceName}?`, ""];
  const volumes = deletableVolumes(plan);
  const binds = mounts(plan.binds);
  if (volumes.length === 0 && binds.length === 0) {
    lines.push("This stops and deletes its container. Nothing else is stored for it.");
    return lines.join("\n");
  }
  lines.push("This stops and deletes its container. Its saved data stays on this computer, so you can set the service up again later.");
  if (volumes.length > 0) {
    lines.push("", "You will be asked next whether to delete that data too. If you keep it, the only way to delete it later is to set the service up again and remove it with its data.");
  }
  if (binds.length > 0) {
    lines.push("", "These folders are never touched:");
    for (const bind of binds) lines.push(`  ${bind.source}`);
  }
  return lines.join("\n");
}

/**
 * The second question: also delete the saved data.
 *
 * Returns null when the service has no named volume, so the app never asks a question
 * whose answer changes nothing. Cancel keeps the data, which is said out loud: a dialog
 * where the safe choice is the unlabelled one is a trap.
 */
export function deleteDataConfirmText(serviceName: string, plan: RemovalPlan): string | null {
  const volumes = deletableVolumes(plan);
  if (volumes.length === 0) return null;

  const lines = [`Also delete ${serviceName}'s saved data?`, ""];
  if (planHasCritical(plan)) {
    lines.push("Some of it cannot be recovered. There is no backup and no way to recreate it.");
  } else {
    lines.push("The service would start from scratch the next time you deploy it.");
  }
  lines.push("", "What would be deleted:");
  for (const volume of volumes) {
    lines.push(`  ${volume.source} (${volume.target})`);
    if (volume.holds) lines.push(`    ${volume.holds}`);
  }
  lines.push("", "Choose Cancel to keep it.");
  return lines.join("\n");
}

/** How far the user agreed to go. */
export type RemovalChoice = { deleteData: boolean; allowCritical: boolean };

/**
 * The two answers, turned into what the backend is allowed to do.
 *
 * Deleting data the catalog marks unrecoverable takes an extra yes that the runtime
 * checks for separately, and that yes is what the second question already asked for:
 * it names every volume and quotes what each one holds, so a user who says yes to it
 * has been told exactly what is lost. A third dialog on top would add no information,
 * and a dialog that adds no information is one people learn to click through.
 *
 * Saying no to the second question withdraws the permission entirely, which is why
 * allowCritical can never be true on its own.
 */
export function removalChoice(plan: RemovalPlan, deleteData: boolean): RemovalChoice {
  return {deleteData, allowCritical: deleteData && planHasCritical(plan)};
}

interface FirewallActions {
  allow(): Promise<unknown>;
  refresh(): Promise<void>;
  setOutput(message: string): void;
  notice(message: string): void;
}

export function wireServerCommandsFirewall(scope: ParentNode, actions: FirewallActions): void {
  scope.querySelector<HTMLButtonElement>("[data-server-commands-firewall]")?.addEventListener("click", () => {
    actions.setOutput("Waiting for the Windows administrator prompt...");
    void actions.allow().then(
      () => {
        actions.setOutput("Windows Firewall now lets your CashPilot reach this computer.");
        return actions.refresh();
      },
      (error) => actions.notice(String(error)),
    );
  });
}

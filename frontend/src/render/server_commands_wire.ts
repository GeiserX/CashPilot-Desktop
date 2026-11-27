import { t } from "./i18n.js"; // .js so the emitted ESM resolves in Node

interface FirewallActions {
  allow(): Promise<unknown>;
  refresh(): Promise<void>;
  setOutput(message: string): void;
  notice(message: string): void;
}

export function wireServerCommandsFirewall(scope: ParentNode, actions: FirewallActions): void {
  scope.querySelector<HTMLButtonElement>("[data-server-commands-firewall]")?.addEventListener("click", () => {
    actions.setOutput(t("server_commands_wire.waiting"));
    void actions.allow().then(
      () => {
        actions.setOutput(t("server_commands_wire.allowed"));
        return actions.refresh();
      },
      (error) => actions.notice(String(error)),
    );
  });
}

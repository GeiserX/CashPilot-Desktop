# Written by packaging/render.sh in https://github.com/GeiserX/CashPilot-Desktop; copy it to the tap's Casks/.
cask "cashpilot-desktop" do
  version "0.32.0"
  sha256 "34e71931929dbc090f585a80794b952d6f5a91ac2cf127d95fc616dab828fe6f"

  url "https://github.com/GeiserX/CashPilot-Desktop/releases/download/v#{version}/CashPilot-Desktop-darwin-arm64.zip"
  name "CashPilot Desktop"
  desc "Deploy and monitor passive-income and DePIN services in Docker or Podman"
  homepage "https://github.com/GeiserX/CashPilot-Desktop"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on arch: :arm64
  depends_on :macos

  app "cashpilot-desktop.app"

  uninstall launchctl: "cloud.cashpilot.desktop.helper"

  zap trash: [
    "~/Library/Application Support/com.cashpilot.desktop",
    "~/Library/LaunchAgents/cloud.cashpilot.desktop.helper.plist",
    "~/Library/Preferences/com.wails.cashpilot-desktop.plist",
    "~/Library/WebKit/com.wails.cashpilot-desktop",
  ]
end

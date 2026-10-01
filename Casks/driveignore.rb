cask "driveignore" do
  version "1.2.0"

  on_arm do
    sha256 "1a981bc2ffb5a1a66f0d516944cf01f52f085d8d60d9d2a34cf59b7bf50f0b96"
    url "https://github.com/ranokay/driveignore/releases/download/v1.2.0/driveignore_1.2.0_darwin_arm64.tar.gz"
  end

  on_intel do
    sha256 "c558b6849733b56dc0fa3e921599de5c9cd4b3c75e2275413ee664d6c6439320"
    url "https://github.com/ranokay/driveignore/releases/download/v1.2.0/driveignore_1.2.0_darwin_amd64.tar.gz"
  end

  name "driveignore"
  desc "Hardlink files into Google Drive while honoring .driveignore"
  homepage "https://github.com/ranokay/driveignore"
  binary "driveignore"

  postflight do
    system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/driveignore"]
  end
end

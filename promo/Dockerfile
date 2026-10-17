# syntax=docker/dockerfile:1.7
# The promo's toolbox (scripts/promo): Playwright's Chromium for recording
# and for Remotion's renders, plus ffmpeg, tmux (the TUI's terminal) and
# gifski (the GIF). Everything pinned.
FROM mcr.microsoft.com/playwright@sha256:35246d87a7c88ea9b771c65d33171b2611b02a8253b4b12ce6f94376c55f99f2
# v1.56.0-noble
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg tmux xz-utils \
 && rm -rf /var/lib/apt/lists/*
ADD --checksum=sha256:b9b6591aa163123d737353d9c8581efdf3234d28eeaa45329b31da905cd5a996 \
  https://github.com/ImageOptim/gifski/releases/download/1.34.0/gifski-1.34.0.tar.xz /tmp/gifski.tar.xz
RUN tar -xJf /tmp/gifski.tar.xz -C /tmp linux/gifski 2>/dev/null; install /tmp/linux/gifski /usr/local/bin/gifski \
 && rm -rf /tmp/linux /tmp/gifski.tar.xz && gifski --version

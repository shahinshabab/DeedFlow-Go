# The stock selenium/standalone-chrome image has no Indic-script fonts, so
# any Malayalam text on a real page (like the government registration
# portal) renders as blank boxes when watched live over noVNC at
# http://localhost:7900 - unreadable, even though the underlying page/DOM
# text itself is perfectly correct Unicode. This just adds font coverage;
# nothing else about the image changes.
FROM selenium/standalone-chrome:127.0

USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends fonts-smc fonts-noto-core \
    && rm -rf /var/lib/apt/lists/*
USER seluser

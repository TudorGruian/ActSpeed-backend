# Piper text-to-speech HTTP server (https://github.com/OHF-Voice/piper1-gpl).
# Piper is GPL-licensed, so it runs as its own service and the backend only
# talks to it over HTTP.
FROM python:3.12-slim

RUN pip install --no-cache-dir "piper-tts[http]"

ENV PIPER_VOICE=en_US-lessac-medium
# Further voices the backend may ask for by name (one per language).
ENV PIPER_EXTRA_VOICES=ro_RO-mihai-medium
VOLUME /voices
EXPOSE 5000

# Download the voices on first start (cached in the volume), then serve.
CMD ["sh", "-c", "python -m piper.download_voices --data-dir /voices \"$PIPER_VOICE\" $PIPER_EXTRA_VOICES && exec python -m piper.http_server -m \"$PIPER_VOICE\" --data-dir /voices --host 0.0.0.0 --port 5000"]

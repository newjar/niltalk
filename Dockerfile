FROM alpine:latest AS deploy
WORKDIR /niltalk
COPY niltalk .
COPY config.toml.sample config.toml
COPY config.toml.sample config.toml.sample
COPY static/ static/
CMD [ "./niltalk" ]

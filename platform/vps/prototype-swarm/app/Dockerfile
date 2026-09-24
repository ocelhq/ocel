FROM scratch
COPY app /app
HEALTHCHECK --interval=2s --timeout=2s --start-period=30s --start-interval=1s --retries=2 CMD ["/app", "probe"]
ENTRYPOINT ["/app"]

# Optimized Keycloak image: build-time options are baked so `start --optimized`
# boots fast and the realm lives under /auth (single public hostname).
FROM quay.io/keycloak/keycloak:26.8.0 AS builder
ENV KC_DB=postgres \
    KC_HEALTH_ENABLED=true \
    KC_METRICS_ENABLED=false \
    KC_CACHE=local \
    KC_HTTP_RELATIVE_PATH=/auth \
    KC_HTTP_MANAGEMENT_RELATIVE_PATH=/
RUN /opt/keycloak/bin/kc.sh build

FROM quay.io/keycloak/keycloak:26.8.0
COPY --from=builder /opt/keycloak/ /opt/keycloak/
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
CMD ["start", "--optimized"]

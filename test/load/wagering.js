// Load test of the service capacity: tokens and wallet setup go through the edge;
// submissions go round-robin straight to api-1..3 (the edge rate limit, 200 req/s
// per source, would otherwise be what gets measured from a single k6 source).
//   make load-test   (stack up; k6 runs in a container on jungle_net)
// Mix: 90% new BET 1.00 on a random wallet, 10% replays of the VU's previous bet
// (idempotency path). Wallets are funded so business rejections do not dominate.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://edge:8080';
const clients = JSON.parse(open('/secrets/clients.json'));
const WALLETS = Number(__ENV.WALLETS || 50);
const APIS = (__ENV.API_URLS || 'http://api-1:8080,http://api-2:8080,http://api-3:8080').split(',');
let previous = null; // per-VU state: last submitted bet (replayed with the same payload)

const replays = new Counter('jungle_replays');
const rejected = new Counter('jungle_rejected');
const unavailable = new Counter('jungle_unavailable');
const conflicts = new Counter('jungle_conflicts');

export const options = {
  scenarios: {
    bets: { executor: 'constant-vus', vus: Number(__ENV.VUS || 20), duration: __ENV.DURATION || '60s' },
  },
  // Regression gates (k6 exits non-zero when crossed); override per environment.
  thresholds: {
    http_req_failed: [`rate<${__ENV.MAX_FAILED_RATE || 0.01}`],
    'http_req_duration{name:submit}': [`p(99)<${__ENV.MAX_P99_MS || 1500}`],
    checks: [`rate>${__ENV.MIN_CHECK_RATE || 0.99}`],
  },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};

function token(id) {
  const res = http.post(`${BASE}/auth/realms/jungle/protocol/openid-connect/token`,
    { grant_type: 'client_credentials', client_id: id, client_secret: clients[id].client_secret });
  return res.json('access_token');
}

export function setup() {
  const internal = token('jungle-internal');
  const wallets = [];
  for (let i = 0; i < WALLETS; i++) {
    const player = `${crypto.randomUUID()}`;
    const r = http.post(`${BASE}/wallets`, JSON.stringify({ playerId: player, initialBalance: { amount: '1000000.00', currency: 'BRL' } }),
      { headers: { Authorization: `Bearer ${internal}`, 'Content-Type': 'application/json' } });
    wallets.push({ id: r.json('id'), player });
  }
  return { wallets, provider: token('provider-a') };
}

export default function (data) {
  let bet = previous;
  if (!bet || Math.random() >= 0.1) {
    const w = data.wallets[Math.floor(Math.random() * data.wallets.length)];
    bet = { ext: `load-${__VU}-${__ITER}-${Date.now()}`, w };
  }
  previous = bet;
  const body = JSON.stringify({ providerId: 'provider-a', externalTransactionId: bet.ext, playerId: bet.w.player, walletId: bet.w.id,
    roundId: 'load', gameId: 'load', kind: 'BET', money: { amount: '1.00', currency: 'BRL' } });
  const api = APIS[(__VU + __ITER) % APIS.length];
  const res = http.post(`${api}/wagering/transactions`, body, { tags: { name: 'submit' },
    headers: { Authorization: `Bearer ${data.provider}`, 'Content-Type': 'application/json', 'Idempotency-Key': `provider-a:${bet.ext}` } });
  if (res.status === 200 && res.json('idempotentReplay') === true) replays.add(1);
  if (res.status === 422) rejected.add(1);
  if (res.status === 409) conflicts.add(1);
  if (res.status === 503) unavailable.add(1);
  check(res, { 'status is 200': (r) => r.status === 200 });
}

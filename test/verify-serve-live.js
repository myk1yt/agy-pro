const http = require('http');
const serveModule = require('../src/serve');

async function testServer() {
  const testPort = 18787;
  const server = await serveModule.startDashboardServer({
    port: testPort,
    open: false
  });

  try {
    // 1. Test GET /
    const htmlRes = await new Promise((resolve, reject) => {
      http.get(`http://127.0.0.1:${testPort}/`, (res) => {
        let body = '';
        res.on('data', c => body += c);
        res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body }));
      }).on('error', reject);
    });
    console.log('GET / Status:', htmlRes.status);
    console.log('GET / Content-Type:', htmlRes.headers['content-type']);
    console.log('GET / Contains <!DOCTYPE html>:', htmlRes.body.includes('<!DOCTYPE html>'));
    console.log('GET / Contains Antigravity:', htmlRes.body.includes('Antigravity'));

    // 2. Test GET /data.json
    const dataRes = await new Promise((resolve, reject) => {
      http.get(`http://127.0.0.1:${testPort}/data.json`, (res) => {
        let body = '';
        res.on('data', c => body += c);
        res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body }));
      }).on('error', reject);
    });
    console.log('GET /data.json Status:', dataRes.status);
    console.log('GET /data.json Content-Type:', dataRes.headers['content-type']);
    const parsed = JSON.parse(dataRes.body);
    console.log('GET /data.json Valid JSON with totalTokens:', parsed.summary && parsed.summary.totalTokens);
  } finally {
    await serveModule.stopDashboardServer(server);
    console.log('Server closed gracefully');
  }
}

testServer().catch(err => {
  console.error('Test server error:', err);
  process.exit(1);
});

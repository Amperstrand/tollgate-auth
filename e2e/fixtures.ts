import { execSync } from 'child_process';

const MINT_URL = process.env.CASHU_MINT_URL || 'https://testnut.cashu.space';
const WALLET_NAME = process.env.CASHU_WALLET_NAME || 'e2e-playwright';

export async function mintTestToken(amountSat: number = 5): Promise<string> {
  const wallet = `e2e-${Date.now()}`;

  execSync(
    `cashu -h ${MINT_URL} -w ${wallet} invoice ${amountSat} 2>&1`,
    { timeout: 30_000, stdio: 'pipe' }
  );

  const sendOutput = execSync(
    `cashu -h ${MINT_URL} -w ${wallet} send ${amountSat} 2>&1`,
    { timeout: 30_000, stdio: 'pipe', encoding: 'utf-8' }
  );

  const match = sendOutput.match(/cashuB[A-Za-z0-9_-]+/);
  if (!match) throw new Error(`Failed to extract Cashu token from CLI output:\n${sendOutput}`);
  return match[0];
}

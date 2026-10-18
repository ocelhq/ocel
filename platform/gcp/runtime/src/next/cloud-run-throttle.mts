const noInstanceMessage = "no available instance";

export async function isCloudRunThrottle(response: Response): Promise<boolean> {
  if (response.status !== 429) return false;
  return (await response.clone().text()).includes(noInstanceMessage);
}

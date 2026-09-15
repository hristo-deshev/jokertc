export {};

const API = "https://app.m2mservices.com/CommonAdministrationService/api/v3";
const USERNAME = "mobileteam";
const PASSWORD = "mobileteam";
const IMEI = "860693080378178";
const CAMERA_ID = 1;

async function post(path: string, body: unknown, token?: string): Promise<any> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token !== undefined) {
    headers["M2MOAuth2Token"] = token;
  }

  const response = await fetch(`${API}/${path}`, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
  });

  return JSON.parse(await response.text());
}

const auth = await post("CreateAuthorizationCode", { UserName: USERNAME, UserPass: PASSWORD });
const tokenResponse = await post("CreateAccessToken", { AuthCode: auth.AuthCode });

const livestream = await post(
  "openlivestream",
  {
    IMEI: IMEI,
    CameraId: CAMERA_ID,
  },
  tokenResponse.AccessToken,
);

console.log(JSON.stringify(livestream, null, 2));
console.log(`\nStreamURL: ${livestream.ResponseBody?.StreamURL}`);

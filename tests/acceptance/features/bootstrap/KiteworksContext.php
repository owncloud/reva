<?php

use Behat\Behat\Context\Context;
use Behat\Behat\Hook\Scope\AfterScenarioScope;
use Behat\Behat\Hook\Scope\BeforeScenarioScope;

require_once 'bootstrap.php';

/**
 * Context for kw-backed acceptance tests.
 *
 * Stages fixture content on a real Kiteworks box via the kw-fixture CLI before
 * each @kw-backed scenario and tears it down afterwards.  All WebDAV calls use
 * the admin credentials exposed by FeatureContext.
 *
 * Required env vars: STORAGE_USERS_KITEWORKS_ENDPOINT, STORAGE_USERS_KITEWORKS_API_TOKEN
 * Optional env vars: STORAGE_USERS_KITEWORKS_INSECURE (default false), OCIS_BASE_URL (default http://localhost:20180)
 */
class KiteworksContext implements Context {

	private string $endpoint;
	private string $token;
	private bool $insecure;
	private string $baseUrl;

	private ?string $rootID = null;
	private ?string $lastResponseCode = null;
	private ?string $lastResponseBody = null;
	private ?string $lastPropfindBody = null;

	private FeatureContext $featureContext;

	public function __construct() {
		$this->endpoint = (string) getenv('STORAGE_USERS_KITEWORKS_ENDPOINT');
		$this->token    = (string) getenv('STORAGE_USERS_KITEWORKS_API_TOKEN');
		$this->insecure = strtolower((string) getenv('STORAGE_USERS_KITEWORKS_INSECURE')) === 'true';
		$this->baseUrl  = rtrim((string)(getenv('OCIS_BASE_URL') ?: 'http://localhost:20180'), '/');
	}

	/**
	 * @BeforeScenario
	 */
	public function gatherContexts(BeforeScenarioScope $scope): void {
		$this->featureContext = $scope->getEnvironment()->getContext('FeatureContext');
	}

	/**
	 * @BeforeScenario @kw-backed
	 */
	public function setUpKwFixture(BeforeScenarioScope $scope): void {
		$this->assertKwEnv();
		$this->rootID = $this->kwFixture('setup', 'behat-' . uniqid());
	}

	/**
	 * @AfterScenario @kw-backed
	 */
	public function tearDownKwFixture(AfterScenarioScope $scope): void {
		if ($this->rootID !== null) {
			$this->kwFixture('teardown', $this->rootID);
			$this->rootID = null;
		}
	}

	// ---------------------------------------------------------------------------
	// Given steps — staging
	// ---------------------------------------------------------------------------

	/**
	 * @Given the staged kiteworks space has a file :name with content :content
	 */
	public function stagedFileWithContent(string $name, string $content): void {
		$this->kwFixture('upload', $this->rootID, $name, $content);
	}

	/**
	 * @Given the staged kiteworks space has a folder :path
	 */
	public function stagedFolder(string $path): void {
		$this->kwFixture('mkdir', $this->rootID, $path);
	}

	// ---------------------------------------------------------------------------
	// When steps — WebDAV actions
	// ---------------------------------------------------------------------------

	/**
	 * @When user :user lists the staged kiteworks space root
	 */
	public function userListsStagedSpaceRoot(string $user): void {
		[$code, $body] = $this->webdav(
			'PROPFIND',
			'/dav/spaces/kiteworks!' . $this->rootID . '/',
			['Depth: 1'],
			$user
		);
		$this->lastResponseCode  = $code;
		$this->lastPropfindBody  = $body;
		$this->lastResponseBody  = $body;
	}

	/**
	 * @When user :user downloads :name from the staged kiteworks space
	 */
	public function userDownloadsFromStagedSpace(string $user, string $name): void {
		[$code, $body] = $this->webdav(
			'GET',
			'/dav/spaces/kiteworks!' . $this->rootID . '/' . ltrim($name, '/'),
			[],
			$user
		);
		$this->lastResponseCode = $code;
		$this->lastResponseBody = $body;
	}

	// ---------------------------------------------------------------------------
	// Then steps — assertions
	// ---------------------------------------------------------------------------

	/**
	 * @Then the staged kiteworks space should appear in the spaces listing
	 */
	public function stagedSpaceAppearsInListing(): void {
		[$code, $body] = $this->webdav('PROPFIND', '/dav/spaces/', ['Depth: 1'], 'admin');
		if ($code !== '207') {
			throw new \RuntimeException("PROPFIND /dav/spaces/ returned HTTP $code, expected 207");
		}
		$needle = 'kiteworks!' . $this->rootID;
		if (strpos($body, $needle) === false) {
			throw new \RuntimeException("Space $needle not found in PROPFIND response:\n$body");
		}
	}

	/**
	 * @Then the HTTP status code should be :expected
	 */
	public function httpStatusCodeShouldBe(string $expected): void {
		if ($this->lastResponseCode !== $expected) {
			throw new \RuntimeException(
				"Expected HTTP $expected but got {$this->lastResponseCode}"
			);
		}
	}

	/**
	 * @Then the file content should be :expected
	 */
	public function fileContentShouldBe(string $expected): void {
		if ($this->lastResponseBody !== $expected) {
			throw new \RuntimeException(
				"Expected body " . json_encode($expected)
				. " but got " . json_encode($this->lastResponseBody)
			);
		}
	}

	/**
	 * @Then the response should contain an entry named :name
	 */
	public function responseShouldContainEntryNamed(string $name): void {
		$body = $this->lastPropfindBody ?? $this->lastResponseBody ?? '';
		// Match any <d:href> segment that ends with the given name (with or without trailing slash)
		if (!preg_match('#/' . preg_quote($name, '#') . '/?</[^>]*href>#i', $body)) {
			throw new \RuntimeException(
				"Entry '$name' not found in PROPFIND response:\n$body"
			);
		}
	}

	// ---------------------------------------------------------------------------
	// Helpers
	// ---------------------------------------------------------------------------

	private function assertKwEnv(): void {
		foreach (['STORAGE_USERS_KITEWORKS_ENDPOINT', 'STORAGE_USERS_KITEWORKS_API_TOKEN'] as $var) {
			if (getenv($var) === false || getenv($var) === '') {
				throw new \RuntimeException("$var is not set — required for @kw-backed tests");
			}
		}
	}

	private function kwFixture(string ...$args): string {
		$repoRoot = realpath(__DIR__ . '/../../../../');
		$bin      = "$repoRoot/kw-fixture";
		if (is_executable($bin)) {
			$runner = escapeshellarg($bin);
		} else {
			$runner = 'go run -mod=mod ' . escapeshellarg("$repoRoot/cmd/kw-fixture/");
		}

		$env = 'STORAGE_USERS_KITEWORKS_ENDPOINT='  . escapeshellarg($this->endpoint)
			 . ' STORAGE_USERS_KITEWORKS_API_TOKEN=' . escapeshellarg($this->token);
		if ($this->insecure) {
			$env .= ' STORAGE_USERS_KITEWORKS_INSECURE=true';
		}

		$cmd = "$env $runner " . implode(' ', array_map('escapeshellarg', $args));
		exec($cmd, $out, $rc);
		if ($rc !== 0) {
			throw new \RuntimeException("kw-fixture failed (exit $rc): $cmd");
		}
		return trim(implode('', $out));
	}

	/**
	 * @return array{string, string}  [status_code, body]
	 */
	private function webdav(string $method, string $path, array $extraHeaders, string $user): array {
		$password = $user === $this->featureContext->getAdminUsername()
			? $this->featureContext->getAdminPassword()
			: $this->featureContext->getRegularUserPassword();

		$headers = array_merge(
			[
				'Authorization: Basic ' . base64_encode("$user:$password"),
				'Content-Type: application/xml; charset=utf-8',
			],
			$extraHeaders
		);

		$opts = [
			'http' => [
				'method'           => $method,
				'header'           => implode("\r\n", $headers),
				'ignore_errors'    => true,
				'follow_location'  => 0,
			],
		];

		if ($method === 'PROPFIND' && empty(array_filter($extraHeaders, fn($h) => str_starts_with($h, 'Depth:')))) {
			$opts['http']['header'] .= "\r\nDepth: 0";
		}

		$ctx  = stream_context_create($opts);
		$body = file_get_contents($this->baseUrl . $path, false, $ctx);
		if ($body === false) {
			$body = '';
		}

		// $http_response_header is populated by file_get_contents
		$code = '0';
		foreach ($http_response_header ?? [] as $h) {
			if (preg_match('#^HTTP/\S+ (\d+)#', $h, $m)) {
				$code = $m[1];
			}
		}
		return [$code, $body];
	}
}

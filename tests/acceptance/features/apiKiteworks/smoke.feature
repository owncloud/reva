@api @kw-backed
Feature: Kiteworks space smoke tests

  Scenario: staged kw folder appears in space listing
    When user "admin" lists the staged kiteworks space root
    Then the staged kiteworks space should appear in the spaces listing

  Scenario: uploaded file content round-trips via oCIS WebDAV
    Given the staged kiteworks space has a file "hello.txt" with content "hello kw"
    When user "admin" downloads "hello.txt" from the staged kiteworks space
    Then the HTTP status code should be "200"
    And the file content should be "hello kw"

  Scenario: nested mkdir leaf appears in folder listing
    Given the staged kiteworks space has a folder "docs"
    When user "admin" lists the staged kiteworks space root
    Then the response should contain an entry named "docs"

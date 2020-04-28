def main(ctx):
  before = [
    testing(ctx),
  ]

  stages = []

  after = [
  ]

  return before + stages + after

def testing(ctx):
  return {
    'kind': 'pipeline',
    'type': 'docker',
    'name': 'testing',
    'platform': {
      'os': 'linux',
      'arch': 'amd64',
    },
    'steps': [
      {
        'name': 'build',
        'image': 'golang:1.13',
        'pull': 'always',
        'commands': [
          'make ci',
        ],
        'volumes': [
          {
            'name': 'gopath',
            'path': '/srv/app',
          },
        ],
      },
      {
        'name': 'lint',
        'image': 'golangci/golangci-lint:v1.21.0',
        'pull': 'always',
        'commands': [
          'golangci-lint run',
        ],
        'volumes': [
          {
            'name': 'gopath',
            'path': '/srv/app',
          },
        ],
      },
    ],
    'volumes': [
      {
        'name': 'gopath',
        'temp': {},
      },
      {
        'name': 'uploads',
        'temp': {}
      }
    ],
    'trigger': {
      'ref': [
        'refs/heads/master',
        'refs/tags/**',
        'refs/pull/**',
      ],
    },
  }
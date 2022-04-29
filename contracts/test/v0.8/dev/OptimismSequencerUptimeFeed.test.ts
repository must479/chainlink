import { ethers, network } from 'hardhat'
import { BigNumber, Contract } from 'ethers'
import { expect } from 'chai'
import { SignerWithAddress } from '@nomiclabs/hardhat-ethers/signers'

describe('OptimismSequencerUptimeFeed', () => {
  let flags: Contract
  let l2CrossDomainMessenger: Contract
  let optimismUptimeFeed: Contract
  let accessController: Contract
  let uptimeFeedConsumer: Contract
  let deployer: SignerWithAddress
  let l1Owner: SignerWithAddress
  let l2Messenger: SignerWithAddress
  let dummy: SignerWithAddress
  const gasUsedDeviation = 100

  before(async () => {
    const accounts = await ethers.getSigners()
    deployer = accounts[0]
    l1Owner = accounts[1]
    dummy = accounts[2]

    const l2CrossDomainMessengerFactory = await ethers.getContractFactory(
        "src/v0.8/tests/MockOptimismL2CrossDomainMessenger.sol:MockOptimismL2CrossDomainMessenger",
        deployer
    )

   l2CrossDomainMessenger = await l2CrossDomainMessengerFactory.deploy()
    
    // Pretend we're on L2
    await network.provider.request({
      method: 'hardhat_impersonateAccount',
      params: [l2CrossDomainMessenger.address],
    })
    l2Messenger = await ethers.getSigner(l2CrossDomainMessenger.address)
    // Credit the L2 messenger with some ETH
    await dummy.sendTransaction({
      to: l2Messenger.address,
      value: (await dummy.getBalance()).sub(ethers.utils.parseEther('0.1')),
    })
  })

  beforeEach(async () => {
    const accessControllerFactory = await ethers.getContractFactory(
      'src/v0.8/SimpleWriteAccessController.sol:SimpleWriteAccessController',
      deployer,
    )
    accessController = await accessControllerFactory.deploy()

    const flagsHistoryFactory = await ethers.getContractFactory(
      'src/v0.8/dev/Flags.sol:Flags',
      deployer,
    )
    flags = await flagsHistoryFactory.deploy(
      accessController.address,
      accessController.address,
    )
    await accessController.addAccess(flags.address)

    const optimismSequencerStatusRecorderFactory =
      await ethers.getContractFactory(
        'src/v0.8/dev/OptimismSequencerUptimeFeed.sol:OptimismSequencerUptimeFeed',
        deployer,
      )
    optimismUptimeFeed =
      await optimismSequencerStatusRecorderFactory.deploy(
        flags.address,
        l1Owner.address,
        l2CrossDomainMessenger.address
      )
    // Required for OptimismSequencerUptimeFeed to raise/lower flags
    await accessController.addAccess(optimismUptimeFeed.address)
    // Required for OptimismSequencerUptimeFeed to read flags
    await flags.addAccess(optimismUptimeFeed.address)
    // Set mock sender in mock L2 messenger contract
    await l2CrossDomainMessenger.setSender(l1Owner.address)

    // Deployer requires access to invoke initialize
    await accessController.addAccess(deployer.address)
    // Once OptimismSequencerUptimeFeed has access, we can initialise the 0th aggregator round
    const initTx = await optimismUptimeFeed
      .connect(deployer)
      .initialize()
    await expect(initTx).to.emit(optimismUptimeFeed, 'Initialized')

    // Mock consumer
    const statusFeedConsumerFactory = await ethers.getContractFactory(
      'src/v0.8/tests/FeedConsumer.sol:FeedConsumer',
      deployer,
    )
    uptimeFeedConsumer = await statusFeedConsumerFactory.deploy(
      optimismUptimeFeed.address,
    )
  })

  describe('constants', () => {
    it('should have the correct value for FLAG_L2_SEQ_OFFLINE', async () => {
      const flag: string =
        await optimismUptimeFeed.FLAG_L2_SEQ_OFFLINE()
      expect(flag.toLowerCase()).to.equal(
        '0x6101bcde09d322cf8ae794576755b23289be35b5',
      )
    })
  })

  describe('#updateStatus', () => {
      it ('should revert if called by an address that is not the L2 Cross Domain Messenger', async () => {
        let timestamp = await optimismUptimeFeed.latestTimestamp()
        expect(optimismUptimeFeed.connect(dummy).updateStatus(true, timestamp)).to.be.revertedWith("InvalidSender")
      })

      it ('should revert if called by an address that is not the L2 Cross Domain Messenger and is not the L1 sender', async () => {
        let timestamp = await optimismUptimeFeed.latestTimestamp()
        await l2CrossDomainMessenger.setSender(dummy.address)
        expect(optimismUptimeFeed.connect(dummy).updateStatus(true, timestamp)).to.be.revertedWith("InvalidSender")
      })

    it(`should update status when status has changed and incoming timestamp is newer than the latest`, async () => {
      let timestamp = await optimismUptimeFeed.latestTimestamp()
      let tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp)
      await expect(tx)
        .to.emit(optimismUptimeFeed, 'AnswerUpdated')
        .withArgs(1, 2 /** roundId */, timestamp)
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(1)

      // Submit another status update, same status, newer timestamp, should ignore
      tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp.add(1000))
      await expect(tx).not.to.emit(optimismUptimeFeed, 'AnswerUpdated')
      await expect(tx).to.emit(optimismUptimeFeed, 'UpdateIgnored')
      expect(await optimismUptimeFeed.latestAnswer()).to.equal('1')
      expect(await optimismUptimeFeed.latestTimestamp()).to.equal(
        timestamp,
      )

      // Submit another status update, different status, newer timestamp should update
      timestamp = timestamp.add(2000)
      tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(false, timestamp)
      await expect(tx)
        .to.emit(optimismUptimeFeed, 'AnswerUpdated')
        .withArgs(0, 3 /** roundId */, timestamp)
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(0)
      expect(await optimismUptimeFeed.latestTimestamp()).to.equal(
        timestamp,
      )
    })

    it(`should update status when status has changed and incoming timestamp is the same as latest`, async () => {
      const timestamp = await optimismUptimeFeed.latestTimestamp()
      let tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp)
      await expect(tx)
        .to.emit(optimismUptimeFeed, 'AnswerUpdated')
        .withArgs(1, 2 /** roundId */, timestamp)
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(1)

      // Submit another status update, same status, same timestamp, should ignore
      tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp)
      await expect(tx).not.to.emit(optimismUptimeFeed, 'AnswerUpdated')
      await expect(tx).to.emit(optimismUptimeFeed, 'UpdateIgnored')
      expect(await optimismUptimeFeed.latestAnswer()).to.equal('1')
      expect(await optimismUptimeFeed.latestTimestamp()).to.equal(
        timestamp,
      )

      // Submit another status update, different status, same timestamp should update
      tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(false, timestamp)
      await expect(tx)
        .to.emit(optimismUptimeFeed, 'AnswerUpdated')
        .withArgs(0, 3 /** roundId */, timestamp)
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(0)
      expect(await optimismUptimeFeed.latestTimestamp()).to.equal(
        timestamp,
      )
    })

    it('should ignore out-of-order updates', async () => {
      const timestamp = (
        await optimismUptimeFeed.latestTimestamp()
      ).add(10_000)
      // Update status
      let tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp)
      await expect(tx)
        .to.emit(optimismUptimeFeed, 'AnswerUpdated')
        .withArgs(1, 2 /** roundId */, timestamp)
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(1)

      // Update with different status, but stale timestamp, should be ignored
      const staleTimestamp = timestamp.sub(1000)
      tx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(false, staleTimestamp)
      await expect(tx)
        .to.not.emit(optimismUptimeFeed, 'AnswerUpdated')
        .withArgs(1, 2 /** roundId */, timestamp)
      await expect(tx).to.emit(optimismUptimeFeed, 'UpdateIgnored')
    })
  })

  describe('AggregatorV3Interface', () => {
    it('should return valid answer from getRoundData and latestRoundData', async () => {
      let [roundId, answer, startedAt, updatedAt, answeredInRound] =
        await optimismUptimeFeed.latestRoundData()
      expect(roundId).to.equal(1)
      expect(answer).to.equal(0)
      expect(answeredInRound).to.equal(roundId)
      expect(startedAt).to.equal(updatedAt) // startedAt = updatedAt = timestamp

      // Submit status update with different status and newer timestamp, should update
      const timestamp = (startedAt as BigNumber).add(1000)
      await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp)
      ;[roundId, answer, startedAt, updatedAt, answeredInRound] =
        await optimismUptimeFeed.getRoundData(2)
      expect(roundId).to.equal(2)
      expect(answer).to.equal(1)
      expect(answeredInRound).to.equal(roundId)
      expect(startedAt).to.equal(timestamp)
      expect(updatedAt).to.equal(startedAt)

      // Check that last round is still returning the correct data
      ;[roundId, answer, startedAt, updatedAt, answeredInRound] =
        await optimismUptimeFeed.getRoundData(1)
      expect(roundId).to.equal(1)
      expect(answer).to.equal(0)
      expect(answeredInRound).to.equal(roundId)
      expect(startedAt).to.equal(updatedAt)

      // Assert latestRoundData corresponds to latest round id
      expect(await optimismUptimeFeed.getRoundData(2)).to.deep.equal(
        await optimismUptimeFeed.latestRoundData(),
      )
    })

    it('should return 0 from #getRoundData when round does not yet exist (future roundId)', async () => {
      const [roundId, answer, startedAt, updatedAt, answeredInRound] =
        await optimismUptimeFeed.getRoundData(2)
      expect(roundId).to.equal(2)
      expect(answer).to.equal(0)
      expect(startedAt).to.equal(0)
      expect(updatedAt).to.equal(0)
      expect(answeredInRound).to.equal(2)
    })
  })

  describe('Protect reads on AggregatorV2V3Interface functions', () => {
    it('should disallow reads on AggregatorV2V3Interface functions when consuming contract is not whitelisted', async () => {
      // Sanity - consumer is not whitelisted
      expect(await optimismUptimeFeed.checkEnabled()).to.be.true
      expect(
        await optimismUptimeFeed.hasAccess(
          uptimeFeedConsumer.address,
          '0x00',
        ),
      ).to.be.false

      // Assert reads are not possible from consuming contract
      await expect(uptimeFeedConsumer.latestAnswer()).to.be.revertedWith(
        'No access',
      )
      await expect(uptimeFeedConsumer.latestRoundData()).to.be.revertedWith(
        'No access',
      )
    })

    it('should allow reads on AggregatorV2V3Interface functions when consuming contract is whitelisted', async () => {
      // Whitelist consumer
      await optimismUptimeFeed.addAccess(uptimeFeedConsumer.address)
      // Sanity - consumer is whitelisted
      expect(await optimismUptimeFeed.checkEnabled()).to.be.true
      expect(
        await optimismUptimeFeed.hasAccess(
          uptimeFeedConsumer.address,
          '0x00',
        ),
      ).to.be.true

      // Assert reads are possible from consuming contract
      expect(await uptimeFeedConsumer.latestAnswer()).to.be.equal('0')
      const [roundId, answer] = await uptimeFeedConsumer.latestRoundData()
      expect(roundId).to.equal(1)
      expect(answer).to.equal(0)
    })
  })

  describe('Gas costs', () => {
    it('should consume a known amount of gas for updates @skip-coverage', async () => {
      // Sanity - start at flag = 0 (`false`)
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(0)
      let timestamp = await optimismUptimeFeed.latestTimestamp()

      // Gas for no update
      timestamp = timestamp.add(1000)
      const _noUpdateTx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(false, timestamp)
      const noUpdateTx = await _noUpdateTx.wait(1)
      // Assert no update
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(0)
      expect(noUpdateTx.cumulativeGasUsed.toNumber()).to.be.closeTo(
        28300,
        gasUsedDeviation,
      )

      // Gas for update
      timestamp = timestamp.add(1000)
      const _updateTx = await optimismUptimeFeed
        .connect(l2Messenger)
        .updateStatus(true, timestamp)
      const updateTx = await _updateTx.wait(1)
      // Assert update
      expect(await optimismUptimeFeed.latestAnswer()).to.equal(1)
      expect(updateTx.cumulativeGasUsed.toNumber()).to.be.closeTo(
        93015,
        gasUsedDeviation,
      )
    })

    describe('Aggregator interface', () => {
      beforeEach(async () => {
        const timestamp = (
          await optimismUptimeFeed.latestTimestamp()
        ).add(1000)
        // Initialise a round
        await optimismUptimeFeed
          .connect(l2Messenger)
          .updateStatus(true, timestamp)
      })

      it('should consume a known amount of gas for getRoundData(uint80) @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.getRoundData(1),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          31157,
          gasUsedDeviation,
        )
      })

      it('should consume a known amount of gas for latestRoundData() @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.latestRoundData(),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          28523,
          gasUsedDeviation,
        )
      })

      it('should consume a known amount of gas for latestAnswer() @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.latestAnswer(),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          28329,
          gasUsedDeviation,
        )
      })

      it('should consume a known amount of gas for latestTimestamp() @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.latestTimestamp(),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          28229,
          gasUsedDeviation,
        )
      })

      it('should consume a known amount of gas for latestRound() @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.latestRound(),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          28245,
          gasUsedDeviation,
        )
      })

      it('should consume a known amount of gas for getAnswer(roundId) @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.getAnswer(1),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          30799,
          gasUsedDeviation,
        )
      })

      it('should consume a known amount of gas for getTimestamp(roundId) @skip-coverage', async () => {
        const _tx = await l2Messenger.sendTransaction(
          await optimismUptimeFeed
            .connect(l2Messenger)
            .populateTransaction.getTimestamp(1),
        )
        const tx = await _tx.wait(1)
        expect(tx.cumulativeGasUsed.toNumber()).to.be.closeTo(
          30753,
          gasUsedDeviation,
        )
      })
    })
  })
})
